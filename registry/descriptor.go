package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// 服务描述按内容哈希寻址，留在注册它的进程内存里。设计见 docs/registry_descriptor_design.md。
//
// 一条注册记录原来 = 节点（随进程变）+ 描述（metadata 与端点表，只随二进制变），描述占 99%。
// 两者绑在一起，一个进程重启就让所有读注册中心的进程把整份描述重传、重解码、重解析。
// 现在写进后端的记录只带描述的哈希（metadata 键 MetaDescriptor），描述本身由所属进程经
// RPC 端点 DescribeEndpoint 按哈希交出，读的一方校验哈希、解一次、缓存。

const (
	// MetaDescriptor 是瘦身后的记录里放描述哈希的 metadata 键。用 metadata 而不是新字段：
	// 各后端本来就原样传 metadata，编码一行不用改；记录签名本来就覆盖 metadata，签名因此
	// 自动绑定了哈希。
	MetaDescriptor = "volts.desc"

	// DescribeEndpoint 是 RPC 侧按哈希取描述的框架保留端点。
	DescribeEndpoint = "/volts.registry.describe"
)

// DescribeRequest / DescribeResponse 是 DescribeEndpoint 的入参与回包（server 答、client 问）。
// 描述是 JSON 规范字节，用 RawMessage 原样嵌进回包，不再转一道 base64。
type (
	DescribeRequest struct {
		Digests []string `json:"digests"`
	}
	DescribeResponse struct {
		Descs map[string]json.RawMessage `json:"descs"`
	}
)

// descriptor 是规范字节的结构。字段顺序就是 JSON 里的顺序 —— 改它等于改了所有哈希。
type descriptor struct {
	Name      string            `json:"name"`
	Version   string            `json:"version"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Endpoints []*Endpoint       `json:"endpoints,omitempty"`
}

// Describe 返回服务描述的规范字节与哈希。端点顺序取调用方给的（server 注册前已经
// sortEndpoints）；map 键由 encoding/json 排序。同样的描述在任何进程里都算出同一个哈希。
func Describe(s *Service) (raw []byte, digest string, err error) {
	raw, err = json.Marshal(descriptor{Name: s.Name, Version: s.Version, Metadata: s.Metadata, Endpoints: s.Endpoints})
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:]), nil
}

// DigestOf 返回记录里的描述哈希：补全过的看 Digest，瘦身记录看 metadata。没有就是空串。
func DigestOf(s *Service) string {
	if s == nil {
		return ""
	}
	if s.Digest != "" {
		return s.Digest
	}
	return s.Metadata[MetaDescriptor]
}

// GroupKey 是「同名同版本下哪些记录算同一份」的判据：描述哈希相同才合并，没有哈希的
// 旧记录退回 EndpointsKey。瘦身记录的端点表是空的，只看 EndpointsKey 会把端点集不同的
// 两个进程并成一份（见 EndpointsKey 的说明）。
func GroupKey(s *Service) string {
	if d := DigestOf(s); d != "" {
		return "d:" + d
	}
	return "e:" + EndpointsKey(s.Endpoints)
}

// ---------------------------------------------------------------------------
// 本进程注册出去的描述
// ---------------------------------------------------------------------------

var (
	localDescs sync.Map // 哈希 → []byte（规范字节）

	// localRefs 记每个 (服务, 节点) 当前用的是哪个哈希：同一节点换了描述时旧哈希可以放掉。
	localMu   sync.Mutex
	localRefs = map[string]string{}
	localUse  = map[string]int{}

	serving atomic.Bool
)

// SetDescriptorServing 声明本进程会在 RPC 侧答 DescribeEndpoint。只有置位了，注册才剥离描述 ——
// 只跑 HTTP 服务器的进程照旧写完整记录，否则别人取不到它的描述。TCP 服务器构造时置位。
func SetDescriptorServing(on bool) { serving.Store(on) }

// DescriptorServing 报告本进程是否在答描述。
func DescriptorServing() bool { return serving.Load() }

// publishLocal 记下本进程某个节点注册出去的描述。
func publishLocal(service, nodeId, digest string, raw []byte) {
	localDescs.Store(digest, raw)
	key := service + "\x00" + nodeId
	localMu.Lock()
	defer localMu.Unlock()
	old, had := localRefs[key]
	if had && old == digest {
		return
	}
	localRefs[key] = digest
	localUse[digest]++
	if had {
		releaseLocked(old)
	}
}

// unpublishLocal 在节点注销时放掉它占着的描述。
func unpublishLocal(service, nodeId string) {
	key := service + "\x00" + nodeId
	localMu.Lock()
	defer localMu.Unlock()
	if old, had := localRefs[key]; had {
		delete(localRefs, key)
		releaseLocked(old)
	}
}

func releaseLocked(digest string) {
	localUse[digest]--
	if localUse[digest] <= 0 {
		delete(localUse, digest)
		localDescs.Delete(digest)
	}
}

// LocalDescriptors 按哈希交出本进程注册出去的描述（规范字节）。不认识的哈希不在结果里。
// 给 DescribeEndpoint 的处理函数用。
func LocalDescriptors(digests []string) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(digests))
	for _, d := range digests {
		if v, ok := localDescs.Load(d); ok {
			out[d] = v.([]byte)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// 别人的描述：取回、校验、缓存
// ---------------------------------------------------------------------------

// DescriptorSource 向 nodes 里的某个节点要 digests 对应的描述，返回哈希 → 规范字节。
// 可以只回一部分；不必校验（调用方会校验）。由 client 包在 init 里注入。
type DescriptorSource func(ctx context.Context, digests []string, nodes []*Node) (map[string][]byte, error)

var source atomic.Pointer[DescriptorSource]

// SetDescriptorSource 注入「怎么去别的进程取描述」。registry 不能依赖 client（client 依赖
// registry），所以由上层注入，与 SetRecordSecurity 同一种做法。
func SetDescriptorSource(fn DescriptorSource) {
	if fn == nil {
		source.Store(nil)
		return
	}
	source.Store(&fn)
}

// decodedDesc 是解码好的描述。Metadata / Endpoints 是所有补全结果共用的 —— 只读。
type decodedDesc struct {
	version   string
	metadata  map[string]string
	endpoints []*Endpoint
	lastUsed  atomic.Int64 // unix 秒
}

var (
	descCache sync.Map // 哈希 → *decodedDesc

	// failedAt 记每个哈希上一次取失败的时刻：同一哈希 descRetry 内不重取，免得每次读都打一遍。
	failedMu sync.Mutex
	failedAt = map[string]time.Time{}

	lastSweep atomic.Int64
)

const (
	descRetry      = 2 * time.Second
	descFetchLimit = 5 * time.Second
	descIdle       = 30 * time.Minute
)

// decodeDescriptor 校验并解码一份取回的描述：哈希必须对得上，服务名必须是记录上那个。
func decodeDescriptor(name, digest string, raw []byte) (*decodedDesc, error) {
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != digest {
		return nil, fmt.Errorf("descriptor %s of %q: content hash mismatch", short(digest), name)
	}
	var d descriptor
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("descriptor %s of %q: %w", short(digest), name, err)
	}
	if d.Name != name {
		return nil, fmt.Errorf("descriptor %s: declared for %q, referenced by %q", short(digest), d.Name, name)
	}
	eps := d.Endpoints
	return &decodedDesc{version: d.Version, metadata: d.Metadata, endpoints: eps[:len(eps):len(eps)]}, nil
}

func short(d string) string {
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

// cached 返回缓存里的描述并记一次使用。
func cached(digest string) *decodedDesc {
	v, ok := descCache.Load(digest)
	if !ok {
		return nil
	}
	d := v.(*decodedDesc)
	d.lastUsed.Store(time.Now().Unix())
	return d
}

// rememberLocal 把本进程自己注册的描述放进缓存：读自己不必走网络。从规范字节解码而不是
// 直接引用调用方的 Service —— 那份对象之后可能被调用方改掉，缓存里的必须与哈希一致。
// server 每个注册周期都会再注册一遍，已在缓存里就什么都不做。
func rememberLocal(name, digest string, raw []byte) {
	if cached(digest) != nil {
		return
	}
	if d, err := decodeDescriptor(name, digest, raw); err == nil {
		d.lastUsed.Store(time.Now().Unix())
		descCache.Store(digest, d)
	}
}

// sweepIdle 清掉 descIdle 没被用到的描述（换版本后旧哈希不会一直占着）。最多每分钟一次。
func sweepIdle() {
	now := time.Now().Unix()
	if last := lastSweep.Load(); now-last < 60 || !lastSweep.CompareAndSwap(last, now) {
		return
	}
	cut := now - int64(descIdle/time.Second)
	descCache.Range(func(k, v any) bool {
		if d := v.(*decodedDesc); d.lastUsed.Load() < cut {
			if _, local := localDescs.Load(k); !local {
				descCache.Delete(k)
			}
		}
		return true
	})
}

// fetchMu 让同一时刻只有一批在取：并发的读者排队，轮到时先看缓存里是不是已经有了。
var fetchMu sync.Mutex

// fetchDescriptors 把缓存里没有的哈希批量取回来，校验后放进缓存。取不到的留空 —— 调用方
// 把引用它们的记录当作「还没就绪」。want 是哈希 → 引用它的服务名（用于校验），nodes 是
// 哈希 → 声明了它的节点。
func fetchDescriptors(want map[string]string, nodes map[string][]*Node) {
	if len(want) == 0 {
		return
	}
	fetchMu.Lock()
	defer fetchMu.Unlock()

	now := time.Now()
	failedMu.Lock()
	var digests []string
	for d := range want {
		if cached(d) != nil {
			continue // 排队期间别人已经取回来了
		}
		if t, ok := failedAt[d]; ok && now.Sub(t) < descRetry {
			continue
		}
		digests = append(digests, d)
	}
	failedMu.Unlock()
	if len(digests) == 0 {
		return
	}

	// 本进程的先拿走（同进程 memory 后端、或读到自己刚注册的记录）。
	var remote []string
	for _, d := range digests {
		if v, ok := localDescs.Load(d); ok {
			if dd, err := decodeDescriptor(want[d], d, v.([]byte)); err == nil {
				dd.lastUsed.Store(now.Unix())
				descCache.Store(d, dd)
				continue
			}
		}
		remote = append(remote, d)
	}
	if len(remote) == 0 {
		return
	}

	fn := source.Load()
	if fn == nil {
		markFailed(remote, now)
		log.Warnf("registry: %d 份服务描述需要从别的进程取，但没有注入取法（client 包没被链接？）", len(remote))
		return
	}

	// 候选节点：引用了这些哈希的全部节点，去重。DescriptorSource 自己挑能答的（TCP）。
	seen := map[string]bool{}
	var cand []*Node
	for _, d := range remote {
		for _, n := range nodes[d] {
			if n != nil && !seen[n.Id+"@"+n.Address] {
				seen[n.Id+"@"+n.Address] = true
				cand = append(cand, n)
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), descFetchLimit)
	defer cancel()
	got, err := (*fn)(ctx, remote, cand)
	if err != nil && len(got) == 0 {
		markFailed(remote, now)
		log.Warnf("registry: 取 %d 份服务描述失败（这些记录本轮不可见，稍后重试）: %v", len(remote), err)
		return
	}
	var missing []string
	for _, d := range remote {
		raw, ok := got[d]
		if !ok {
			missing = append(missing, d)
			continue
		}
		dd, derr := decodeDescriptor(want[d], d, raw)
		if derr != nil {
			missing = append(missing, d)
			log.Warnf("registry: 丢弃取回的服务描述: %v", derr)
			continue
		}
		dd.lastUsed.Store(now.Unix())
		descCache.Store(d, dd)
	}
	if len(missing) > 0 {
		markFailed(missing, now)
		log.Warnf("registry: %d 份服务描述没取到（引用它们的记录本轮不可见，稍后重试）", len(missing))
	}
}

func markFailed(digests []string, at time.Time) {
	failedMu.Lock()
	for _, d := range digests {
		failedAt[d] = at
	}
	// 失败表本身也别无限长：只留最近的。
	if len(failedAt) > 4096 {
		for d, t := range failedAt {
			if at.Sub(t) > descRetry {
				delete(failedAt, d)
			}
		}
	}
	failedMu.Unlock()
}
