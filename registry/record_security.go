package registry

import (
	"sync"
	"sync/atomic"
	"time"
)

// 注册项的签名与验签：能写注册中心的人不该因此就能把自己插进别人的调用链。
//
// 注册中心里的东西直接决定流量去哪：选点按节点地址拨号，网关按端点建反向代理路由，
// 应用层按服务 metadata 判谁拥有哪个模型、谁给哪个方法提供实现。能写 etcd 就能登记一个
// 冒名的节点、或在 metadata 里声明一段别人的方法 —— 而所有读方照单全收。
//
// RecordSecurity 是**进程级**的两个钩子，由应用决定签什么、认谁（框架不持有密钥）：
//
//   - Sign：一条注册项写入注册中心之前调用。传进来的是**单节点**的注册项，节点是副本，
//     签名写进 Nodes[0].Metadata 即可。返回错误则这一条不写。
//   - Verify：每条从注册中心读出的注册项在**合并之前**调用（同样是单节点 —— 合并后
//     metadata 与端点只剩一份，就对不上每个节点各自签的那份了）。返回错误则这条整个
//     丢弃：不进合并结果、不进缓存、选点看不见它。
//
// 进程级的理由同 transport.SetPeerTLS：注册中心实例按类型与地址缓存、由 server /
// client / broker 各自持有，逐个配一定会漏，漏掉的那一个正好是没验签的读方。
//
// 目前只有 etcd 后端接了这两个钩子（memory / mdns / consul 不调用）。

// RecordSecurity 见文件头。两个钩子都可以为 nil。
type RecordSecurity struct {
	Sign   func(*Service) error
	Verify func(*Service) error
}

var recordSecurity atomic.Pointer[RecordSecurity]

// SetRecordSecurity 装上本进程的注册项签名与验签；nil 等于都不做。
func SetRecordSecurity(s *RecordSecurity) {
	recordSecurity.Store(s)
}

// SignedNode 返回 node 的一份带签名的副本（注册项 s 只取名字、版本、metadata 与端点）。
// 没装 Sign 时原样返回 node。供注册中心后端在写入前调用。
//
// node 必须复制：同一次注册里，一个进程的所有服务共用同一个 *Node（server.Register），
// 而签名是按服务签的。
func SignedNode(s *Service, node *Node) (*Node, error) {
	sec := recordSecurity.Load()
	if sec == nil || sec.Sign == nil {
		return node, nil
	}
	cp := *node
	cp.Metadata = make(map[string]string, len(node.Metadata)+2)
	for k, v := range node.Metadata {
		cp.Metadata[k] = v
	}
	rec := &Service{
		Name:      s.Name,
		Version:   s.Version,
		Metadata:  s.Metadata,
		Endpoints: s.Endpoints,
		Nodes:     []*Node{&cp},
	}
	if err := sec.Sign(rec); err != nil {
		return nil, err
	}
	return &cp, nil
}

// AcceptRecord 报告一条读出的注册项能不能用。没装 Verify 时恒为 true。
// 不能用的按 (服务名, 节点) 节流打一条告警 —— 被丢的那一方自己看不到任何报错。
func AcceptRecord(s *Service) bool {
	sec := recordSecurity.Load()
	if sec == nil || sec.Verify == nil || s == nil {
		return true
	}
	err := sec.Verify(s)
	if err == nil {
		return true
	}
	node := ""
	if len(s.Nodes) > 0 && s.Nodes[0] != nil {
		node = s.Nodes[0].Id + "@" + s.Nodes[0].Address
	}
	if recordLogDue(s.Name + "|" + node + "|" + err.Error()) {
		log.Warnf("registry: 丢弃注册项 %s（节点 %s）: %v", s.Name, node, err)
	}
	return false
}

var (
	recordLogMu sync.Mutex
	recordLogAt = map[string]time.Time{}
)

const recordLogEvery = 10 * time.Minute

func recordLogDue(key string) bool {
	recordLogMu.Lock()
	defer recordLogMu.Unlock()
	if at, ok := recordLogAt[key]; ok && time.Since(at) < recordLogEvery {
		return false
	}
	if len(recordLogAt) > 4096 {
		recordLogAt = map[string]time.Time{}
	}
	recordLogAt[key] = time.Now()
	return true
}
