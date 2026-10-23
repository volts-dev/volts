package router

import (
	"sort"

	"github.com/volts-dev/volts/registry"
)

// 注册数据没变就不重建代理路由。
//
// # 为什么需要它
//
// refresh() 每 60s 对每个远端服务调一次 store()，而 store() 不管内容变没变，都把每个
// HTTP 端点重新 EndpiontToRoute → hasLocalRoute → AddRoute 走一遍。注册中心里绝大部分
// 是 kylin 的记录（2026-10-23 本机：308 条共 8.5MB，kylin 占 7.6MB，154 个服务、每个都带
// 完整端点表），于是**不托管这些服务的进程**（connect_components、websvc）每分钟白建
// 一遍几千条路由：实测 store 一项 ≈ 9.6MB/轮，加上取数时的复制，整轮 ≈ 32MB/分钟。
// kylin 自己的服务被 isLocalService 跳过，所以 kylin 不中招 —— 这就是"只有那两个
// 进程堆来回摆得厉害"的由来。watch 也走这里：一个兄弟进程重启就是几百个事件，每个都
// 触发一次整服务的 store。
//
// # 为什么可以安全地跳过
//
// store() 的输出只取决于三样东西：服务记录里它读的那几个字段（见 proxyFingerprint）、
// 本进程身份（filteSelf 用）、以及树上的本地路由（护盾）。前两样进指纹；本地路由的
// 增减都是"本地后写赢"或护盾自然生效，不需要再建一遍。剩下的风险是**树上的代理路由被
// 别处拿掉了**而指纹还在 —— 此前每 60s 一轮的全量 store 顺带把它补回来（见
// proxy_route_reclaim.go 的"误回收会自愈"）。所以：
//
//   - dropProxyRoutes 时清掉该服务的指纹（服务回来时一定重建）；
//   - store() 回报 deferred（还认不出自己）时不记指纹；
//   - refresh() 每 proxyForceEvery 轮强制全量一次，自愈从 60s 一次放宽到这个周期。
//
// 指纹算错只有一个方向是坏的：该变的没认出来变。所以宁可多算进去 —— 多算的代价只是
// 多跑一轮 store()。

// proxyForceEvery 是 refresh() 强制全量 store() 的轮数间隔（默认 60s 一轮 → 10 分钟）。
const proxyForceEvery = 10

// storeIfChanged 是 refresh() 与 watch() 用的 store()：同一服务的记录与本进程身份都与
// 上次建成时一样就跳过。force 为真时照常全量重建。返回值同 store()。
func (self *TRouter) storeIfChanged(services []*registry.Service, force bool) (deferred bool) {
	name, ok := singleServiceName(services)
	if !ok {
		// 一次传进来几个服务名（store 不假设只有一个），没法按名记账：照旧全量。
		return self.store(services)
	}

	me := self.localIdentity()
	if me == nil {
		// 认不出自己：store() 会整轮推迟，指纹也无从谈起。
		self.proxyFingerprints.Delete(name)
		return self.store(services)
	}

	fp := proxyFingerprint(services, me)
	if !force {
		if v, ok := self.proxyFingerprints.Load(name); ok && v.(uint64) == fp {
			return false
		}
	}

	deferred = self.store(services)
	if deferred {
		self.proxyFingerprints.Delete(name)
	} else {
		self.proxyFingerprints.Store(name, fp)
	}
	return deferred
}

func singleServiceName(services []*registry.Service) (string, bool) {
	name := ""
	for _, s := range services {
		if s == nil {
			continue
		}
		if name == "" {
			name = s.Name
		} else if s.Name != name {
			return "", false
		}
	}
	return name, name != ""
}

// proxyFingerprint 覆盖 store() 读到的全部输入：服务名与版本、每个节点的 id / 地址 /
// metadata（filteHttpNodes 看 protocol，全拿进来更稳）、每个端点的方法与路径，外加本进程
// 身份（filteSelf）。记录的顺序也算进去 —— 顺序变了只会误判成"变了"，那是安全的方向。
//
// 不分配：map 用逐项哈希之和（与遍历顺序无关），身份集合同理。
func proxyFingerprint(services []*registry.Service, me *selfIdentity) uint64 {
	h := fnvOffset
	for _, s := range services {
		if s == nil {
			h = fnvStr(h, "\x01nil")
			continue
		}
		h = fnvStr(h, s.Name)
		h = fnvStr(h, s.Version)
		h = fnvNum(h, uint64(len(s.Nodes)))
		for _, n := range s.Nodes {
			if n == nil {
				h = fnvStr(h, "\x01nil")
				continue
			}
			h = fnvStr(h, n.Id)
			h = fnvStr(h, n.Address)
			h = fnvNum(h, mapHash(n.Metadata))
		}
		if s.Digest != "" {
			// 描述的内容哈希（registry/descriptor.go）已经覆盖了端点表的每一个字节，不必再逐条走。
			h = fnvStr(h, "\x01digest")
			h = fnvStr(h, s.Digest)
			continue
		}
		h = fnvNum(h, uint64(len(s.Endpoints)))
		for _, ep := range s.Endpoints {
			if ep == nil {
				h = fnvStr(h, "\x01nil")
				continue
			}
			h = fnvStr(h, ep.Path)
			h = fnvNum(h, uint64(len(ep.Method)))
			for _, m := range ep.Method {
				h = fnvStr(h, m)
			}
		}
	}
	h = fnvNum(h, setHash(me.ids))
	h = fnvNum(h, setHash(me.addrs))
	return h
}

// FNV-1a，直接在 string 上算：hash.Hash 的 Write([]byte(s)) 经接口调用，每个字符串都会
// 逃逸成一次分配，而这里每轮要喂几千条端点路径。
const (
	fnvOffset uint64 = 14695981039346656037
	fnvPrime  uint64 = 1099511628211
)

func fnvStr(h uint64, s string) uint64 {
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= fnvPrime
	}
	// 分隔符，免得 "ab"+"c" 与 "a"+"bc" 撞上。
	h ^= 0xff
	h *= fnvPrime
	return h
}

func fnvNum(h, v uint64) uint64 {
	for i := 0; i < 8; i++ {
		h ^= (v >> (8 * i)) & 0xff
		h *= fnvPrime
	}
	return h
}

func mapHash(m map[string]string) uint64 {
	var sum uint64
	for k, v := range m {
		sum += fnvStr(fnvStr(fnvOffset, k), v)
	}
	return sum + uint64(len(m))
}

func setHash(m map[string]struct{}) uint64 {
	var sum uint64
	for k := range m {
		sum += fnvStr(fnvOffset, k)
	}
	return sum + uint64(len(m))
}

// fingerprintedServices 只给测试用：指纹表里记着的服务名，按序。
func (self *TRouter) fingerprintedServices() []string {
	var out []string
	self.proxyFingerprints.Range(func(k, _ any) bool {
		out = append(out, k.(string))
		return true
	})
	sort.Strings(out)
	return out
}
