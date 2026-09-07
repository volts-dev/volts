// Package outbound 是**对外抓取**用的 HTTP 客户端：把每一次出站请求伪装成一个
// 具体的浏览器，并可以让它从一个代理出去；多个身份轮换着用。
//
// 谁需要它：任何定时去问别人家接口的东西 —— 库存/价格采集、站点探测、第三方数据
// 同步。它们的共同处境是**从同一个 IP、用同一个 TLS 指纹、带着
// `User-Agent: Go-http-client/1.1`，一天几百上千次**。这三样凑齐就是"一个爬虫"
// 的定义，被限速或封掉的那天，整条采集一起哑掉。
//
// # 与 client 包的分工
//
// volts/client 是**调自己人**的：服务发现、负载均衡、熔断、RPC 编解码，对面是
// 你自己部署的服务。本包是**调外人**的：对面不认识你，也没有义务配合，问题从
// "怎么找到它"变成"它凭什么理你"。两者共用 transport 的拨号与指纹注册表，但
// 上层关心的事完全不同，所以不合并。
//
//	cli := outbound.Client()   // 之后就是普通的 *http.Client
//	rsp, err := cli.Do(req)
//
// # 一个身份 = 指纹 + UA + 那几个头 + 出口 IP
//
// 轮换的单位是**身份**，不是单项。同一个出口 IP 一会儿说自己是 mac Chrome、
// 一会儿是 win Firefox，比一直是同一个更可疑。所以四样在建池时配对钉死：
//
//   - TLS 指纹用 core/ja3 的**预置**（"chrome_133"），不是手抄的 JA3 串 ——
//     JA3 不记录扩展内容，而 GREASE、扩展乱序、后量子 key share 全在内容里；
//     从串重建出来的是"一个谁都不是的客户端"，比 Go 默认的还独特。
//   - UA 由指纹自带（volts/transport 的 Fingerprint 表），**没有手抄的机会**。
//   - sec-ch-ua 那几个头从 UA **推导**，版本永远与 UA 一致（headers.go）。
//   - **每个身份有自己的连接池**：共享一个 Transport 会让一条以 mac Chrome 指纹
//     握过手的连接去发一个 UA 写着 win Chrome 的请求 —— 同一条 TLS 连接上出现
//     两个身份，比不伪装更容易被认出来。
//
// ★ **没有配代理时，轮换的只有指纹，IP 还是同一个。** 站点按 IP 限速的话，
// 指纹换多少条都没用。代理是这件事里唯一能换 IP 的那一半，只能由使用者提供。
//
// # 伪装做得到哪一步（2026-09-02 实测，tls.peet.ws 回显）
//
// **TLS 层：逐字节像。** chrome_133 的 JA4 是
// `t13d1516h2_8daaf6152771_d8a2da3f94cd`，中间那段正是真 Chrome 的 cipher 哈希。
//
// **HTTP/2 层：像不了，而且不该硬凑。** Go 的 x/net/http2 发的 SETTINGS 是
//
//	2:0;4:4194304;5:1048576;6:10485760|1073741824|0|a,m,p,s
//
// 真 Chrome 133 是
//
//	1:65536;2:0;4:6291456;6:262144|15663105|0|m,a,s,p
//
// 把能调的都调到 Chrome 的值，最接近也只到
//
//	2:0;4:6291456;5:16384;6:262144;1:65536|15728640|0|a,m,p,s
//
// —— setting 5 Chrome 根本不发而 Go 必发，SETTINGS 的**顺序**不同，伪头顺序
// `a,m,p,s` 硬编码在 Go 的 encodeHeaders 里改不了。
//
// ★★ 所以**故意不调**。Go 默认的那个 Akamai 指纹是全世界最常见的之一（每个 Go
// 服务都是它，含大量合法的服务端调用）；调完既不像 Chrome，又变成一个**独一无二**
// 的指纹 —— 从"混在人群里"变成"世界上只有你在发"。半吊子的伪装比不伪装更显眼。
// 要真的做 h2 指纹，得换一个能完全控制帧的 HTTP/2 栈，那是另一个量级的依赖。
//
// # 被挡回来之后：403/429 换一个身份重试
//
// 网络错误（连不上、握手失败）与**被拒绝**是两件不同的事，而后者才是伪装真正
// 要面对的：TCP 通了、TLS 握完了、HTTP 也答了 —— 只是答的是 403。站在
// http.RoundTripper 的角度这是一次**成功**的往返，所以不做特殊处理的话，
// 一个被风控挡住的身份会带着同一套指纹、同一个 IP 一遍遍地撞上去。
//
// 所以 RotateOnStatus 里的状态码被当作"这个身份被挡了"：让它进冷却、**扔掉它
// 池子里的空闲连接**（风控的判定常常绑在连接/会话上，留着那条连接冷却就白等了），
// 换下一个身份重发。全部试完还是被挡，就把**最后那个响应**原样交给调用方 ——
// 它需要看见 403 本身，而不是一句被改写过的错误。
//
// 429 的 Retry-After 优先于我们自己的退避：对方说的等待时间比猜的准。
//
// ★★ **没有配代理时，换身份换不掉 IP。** 对方若是按 IP 限速，轮换指纹不会有
// 任何效果 —— 这一点在第一次被挡时会记一条 WARN 说出来，而不是让人对着一个
// "已经在轮换了"的功能猜为什么没用。
//
// # 同一条线路上不并行
//
// 一条**线路** = 一个身份 = 指纹 + UA + 出口 IP。同一条线路上同时在飞多个请求，
// 是最容易招来拒绝的形态之一：h1 会为第二个请求另拨一条 TCP 连接，h2 会在同一
// 条连接上开第二条流 —— 对方看到的都是"同一个浏览器、同一个 IP，在同一毫秒里
// 发出了几个互不相关的请求"。真人不会这样用浏览器，而这恰恰是很容易统计的信号。
//
// 所以每个身份有一个容量为 1 的闸门（MaxPerIdentity 可调）：占不到位置的请求
// **去等**，而不是从同一个身份上再发一个。并发不是被取消了，而是被引到别的线路
// 上去 —— 有几个代理就有几条线路，它们之间照常并行。这也是"加并发"的正确做法：
// 加一个出口，而不是把闸门开大。
//
// ★ 位置留到**响应体关闭**才放，不是拿到响应头就放：响应头回来时那条连接还在
// 传 body，此刻放行下一个请求，对方看到的仍然是两个重叠的请求。代价是忘了
// Close(Body) 的调用方会堵住一条线路 —— maxSlotHold 到点强行放开兜底。
//
// ★ 没有代理时只有一条线路，于是**所有出站请求都串行**。这是有意的：一个出口
// 上的并发本来就无处可藏。嫌慢就把轮询周期调长，或者配一个代理出口。
//
// # 常备身份与备用身份
//
// 上面那条"一个出口一个身份"是**稳态**的规矩：一个真人不会每五分钟换一次浏览器。
// 但一旦被挡，"看起来自然"这件事已经失败了，此时的选择只剩下"继续用一个确定
// 不通的身份"和"换一个还有机会的"。
//
// 于是多出来的指纹不丢掉，而是作为**备用身份**留着：平时一次都不用（稳态仍然是
// 一个出口一个身份），只有常备的全在冷却里时才顶上。显式配了 profiles 就没有
// 备用 —— 那是"我就要这几个"的明确意图。
//
// # 配置
//
//	"outbound": {
//	  "profiles": ["chrome_133", "firefox_120"],
//	  "proxies": ["socks5://127.0.0.1:1086", "http://user:${PROXY_PASS}@gw:8080"],
//	  "allow_direct": false,
//	  "cooldown": 30000,
//	  "rotate_on_status": [403, 429],
//	  "max_per_identity": 1
//	}
//
// 代理密码写 `${PROXY_PASS}` 从环境变量取（config 包的能力）—— 配置文件要进备份
// 和 git，代理凭据不该躺在里面。
//
// ★★ **配了代理就绝不偷偷直连。** `allow_direct` 默认 false：代理全部不可用时
// 出站直接失败（ErrNoUsableProxy），而不是退回本机出口。会配代理的人多半正是为了
// 不让对方看见自己的 IP，静默直连把这件事悄悄反过来做了，而他不会知道 ——
// 同 volts transport.ErrJa3NotRegistered 的取舍。
//
// # 启用指纹要空导入 core/ja3
//
// TLS 指纹的实现在 core/ja3 —— 一个**独立的 Go 子模块**（它依赖 utls，一个用途为
// 模仿其他客户端指纹的库，绝大多数写业务服务的人不需要，却会被无条件编进二进制并
// 在合规扫描里被标记）。
//
// 本包只读 volts/transport 的注册表，所以**主模块的依赖树里没有 utls** ——
// 用 outbound 的人不会因此被迫带上它。
//
//	import _ "github.com/volts-dev/volts/core/ja3"   // 在需要伪装的那个服务的 main/init 里
//
// 没导入时指纹表是空的，本包**明确告知**（日志 + Status()），然后按不伪装走 ——
// 而不是悄悄地什么都没做。
package outbound

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/volts-dev/volts/config"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/transport"
)

var log = logger.New("outbound")

// ErrNoUsableProxy —— 配了代理，但一个都建不起来，而 allow_direct 是 false。
//
// 这是**拒绝出站**，不是一次失败的请求：直连会把真实 IP 送出去，而那正是配代理
// 要避免的事。调用方会把它记进自己的错误列，人在界面上看得到。
var ErrNoUsableProxy = errors.New("core/outbound: 配置的代理一个都不可用，且未允许直连（allow_direct=false）")

type (
	// Config 是本包在 config.json 里的那一节。
	Config struct {

		// Profiles 要用的 TLS 指纹（core/ja3 的预置名，如 "chrome_133"）。
		//
		// 留空 = 用**全部**已注册的指纹。这是有意的默认：身份越多，单个身份的
		// 请求频率越低。写死一个列表是"我只想要 Chrome"这种明确意图才需要的。
		//
		// ★ 名字写错不会静默跳过 —— 会记一条 WARN 并列出可用的名字。
		Profiles []string

		// Proxies 代理 URL 列表。支持 http://、https://（CONNECT 隧道）与
		// socks5://（= socks5h，域名交给代理解析）。可带 user:pass。空 = 直连。
		Proxies []string

		// AllowDirect 代理全部不可用时是否允许直连。默认 false，见包注释。
		AllowDirect bool

		// Cooldown 一个身份连续失败后的冷却起点（之后指数退避，上限 5 分钟）。
		Cooldown time.Duration

		// RotateOnStatus 哪些状态码算"这个身份被挡了"：让它冷却并换一个身份重发。
		//
		// 默认 403（Forbidden，风控拒绝最常见的形态）与 429（Too Many Requests）。
		//
		// ★ **503 不在默认里。** 它既是 Cloudflare 挑战页的状态码，也是"站点真的
		// 挂了"的状态码，而这两者在响应里分不开。把它算作被挡，会在对方本就吃力
		// 的时候把请求量乘以 maxAttempts —— 明确知道自己面对的是挑战页时再配上。
		//
		// 置空 = 关掉这个行为，只在网络错误时换身份。
		RotateOnStatus []int

		// MaxPerIdentity 同一个身份上允许**同时在飞**的请求数。默认 1，即同一条
		// 线路上的请求串行发出。见包注释「同一条线路上不并行」。
		//
		// ★ 调大它就是在把那件招来拒绝的事又做回去。会需要调的只有一种情况：
		// 明确知道对方不介意并发，而串行让采集赶不上周期 —— 那也该先把
		// 周期调长，或者加一个代理出口（多一条线路才是真正的加并发）。
		MaxPerIdentity int
	}
)

// slots 每个身份的并发闸门容量。0（没配）当 1。
func (c *Config) slots() int {
	if c.MaxPerIdentity > 0 {
		return c.MaxPerIdentity
	}
	return 1
}

// rotatesOn 判一个状态码算不算"被挡"。
func (c *Config) rotatesOn(code int) bool {
	for _, s := range c.RotateOnStatus {
		if s == code {
			return true
		}
	}
	return false
}

const (
	// cooldownMax 是冷却的上限。再长就等于把一个身份永久踢出去 —— 而代理挂掉
	// 多半是暂时的（重启、换线路），没有理由不再试。
	cooldownMax = 5 * time.Minute

	// maxAttempts 一次请求最多换几个身份重试。
	//
	// 3 不是量出来的：调用方给的 ctx 通常只有几十秒，每次重试都要重新握手，
	// 试太多只会把这一次请求的预算耗光而不换来任何信息 —— 三个身份都连不上，
	// 第四个大概率也一样，那是网络或对方的问题，不是身份的问题。
	maxAttempts = 3

	// idlePoll 等一个身份空出来时的重试间隔。
	//
	// 释放会主动通知（r.idle），这个轮询只是**兜底**：冷却到期没有事件可通知
	// （它是一个时刻，不是一个动作），而丢一次通知就等于把一个请求挂到 ctx 超时。
	idlePoll = 50 * time.Millisecond
)

// maxSlotHold 一个位置最多被占多久。
//
// ★ 这是**泄漏阀，不是策略**。位置在响应体关闭时才释放（见 slotBody），于是
// 一个忘了 Close 的调用方会把整条线路永久堵死 —— 在只有一个身份的部署里就是
// 整个出站永久堵死。原本忘关 Body 只泄漏一条连接，不该被本包升级成停摆。
// 到点强行放掉，宁可退回并发也不要卡死。
//
// 是变量只为让测试能把它调短，生产路径上没有任何地方改它。
var maxSlotHold = 2 * time.Minute

var cfg = &Config{
	AllowDirect:    false,
	Cooldown:       30 * time.Second,
	RotateOnStatus: []int{http.StatusForbidden, http.StatusTooManyRequests},
	MaxPerIdentity: 1,
}

func init() { config.Bind("outbound", cfg) }

var (
	clientOnce sync.Once
	client     *http.Client
	shared     *rotator
)

// Client 返回出站客户端。
//
// ★ 返回的是**同一个**客户端。每调一次建一个的话，每份都有自己的连接池和自己的
// 一套冷却状态：对方看到的连接数翻倍（这正是"会不会被当成爬虫"的那个信号），
// 而一个已经被限速的身份在另一份池子里还是"健康"的。
//
// 三个超时都显式给了值：net/http 的零值 Transport **没有**响应头超时，一个只
// 建连不回话的站点会把工作池里的一个位置占到天荒地老（调用方的 ctx 会取消请求，
// 但那已经是几十秒之后的事，而池子只有几个位置）。
func Client() *http.Client {
	clientOnce.Do(func() {
		shared = &rotator{}
		client = &http.Client{
			Timeout:   30 * time.Second, // 兜底；真正的期限来自调用方的 ctx
			Transport: shared,
		}
	})
	return client
}

// Status 报告当前身份池的样子，给启动日志和诊断端点用。
//
// 存在的理由：伪装失效是**静默**的 —— 没导入 core/ja3、指纹名写错、代理连不上，
// 表现都只是"请求照常发出去了"。让人能一眼看到"现在到底以什么身份在出门"。
func Status() []string {
	r := Client().Transport.(*rotator)
	r.once.Do(r.build)

	// ★ 加锁读：SetProxies 会在运行期整体换掉这两个切片（见 setProxies）。
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.buildErr != nil {
		return []string{"出站不可用: " + r.buildErr.Error()}
	}
	now := time.Now()
	out := make([]string, 0, len(r.personas)+len(r.reserves))
	for _, p := range r.personas {
		out = append(out, p.status(now))
	}
	for _, p := range r.reserves {
		out = append(out, "备用 "+p.status(now))
	}
	return out
}

// Concurrency 报告本客户端**真正允许**同时在飞的出站请求数：常备线路数 × 每条
// 线路的闸门容量。
//
// 存在的理由：调用方普遍有自己的工作池（"同时侦测几个站点"），而闸门让那个数字
// 不再等于实际并发 —— 池子开 4 而只有 1 条线路时，3 个 worker 只是在排队，
// 且这段排队**算在调用方自己的 ctx 里**，于是一个本来够用的超时会变成"对方超时"，
// 而真正的原因在我们这边。用这个数去定池子大小，两边就对齐了。
//
// ★ 它会变：SetProxies 换一批出口之后线路数就变了。别在启动时读一次存起来。
func Concurrency() int {
	r := Client().Transport.(*rotator)
	r.once.Do(r.build)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.buildErr != nil || len(r.personas) == 0 {
		return 1
	}
	return len(r.personas) * cfg.slots()
}

// SetProxies 用**运行时**的代理列表替换配置文件里那一份，并立刻重建身份池。
//
// 存在的理由：代理是"哪些出口能用"的日常运维，而 config.json 改一次要重启进程 ——
// 一个必须重启才能换出口的功能，在被限速的当天没有任何用处。上层（例如
// connect_components 的「出站代理」设置页）在启动时与每次改动后调它一次。
//
// 语义：
//
//   - urls 为 nil ⇒ **撤销**覆盖，回到 config.json 的 outbound.proxies。
//   - urls 为空切片 ⇒ 覆盖成"一个代理都没有"，即直连（与配置文件里留空等价）。
//     ★ 上层要推的是**启用**的节点，不是**测通**的节点：把测速失败的过滤掉，
//     一次全网抖动就会让列表变空，于是"配了代理"悄悄变成"直连"，而 allow_direct
//     的整个用意就是不让这件事发生。能不能连由轮换与冷却去处理。
//   - 返回错误时**旧池子原样保留**。新列表一个都建不起来（且 allow_direct=false）
//     时，让正在工作的出口继续工作，比换上一个空池子把出站全掐掉要好；错误交给
//     调用方显示给人看。
func SetProxies(urls []string) error {
	r := Client().Transport.(*rotator)
	r.once.Do(r.build)
	return r.setProxies(urls)
}

// setProxies 建新池子 → 换上 → 关掉旧池子的空闲连接。
func (r *rotator) setProxies(urls []string) error {
	var next *[]string
	if urls != nil {
		cp := append([]string(nil), urls...)
		next = &cp
	}

	// ★ 先落覆盖值再 buildPersonas —— resolveProxies 读的就是它。失败时要还原，
	// 否则一次失败的保存会把覆盖值留在一个"下次重建才会生效"的半吊子状态。
	prev := runtimeProxies.Swap(next)
	active, reserve, err := buildPersonas(cfg)
	if err != nil {
		runtimeProxies.Store(prev)
		return err
	}

	r.mu.Lock()
	old := r.all()
	r.personas, r.reserves = active, reserve
	r.allDirect = allDirect(r.all())
	r.buildErr = nil
	r.next, r.nextRsv = 0, 0
	r.warned = false // 换了一批出口，"没有代理"那句警告值得再说一次
	r.mu.Unlock()

	// ★ 关掉旧身份的空闲连接。不关的话，被换下去的代理上那几条 keep-alive 会
	// 一直挂到 IdleConnTimeout —— 人在界面上"停用"了一个节点，而它还在往外发。
	for _, p := range old {
		p.closeIdle()
	}

	labels := make([]string, 0, len(active))
	for _, p := range active {
		labels = append(labels, p.label)
	}
	log.Dbgf("出站代理已更新：常备 %d 个（%s），备用 %d 个",
		len(active), strings.Join(labels, ", "), len(reserve))
	return nil
}

// rotator 是按身份轮换的 RoundTripper。
//
// 池子**惰性构建**（首次请求时），不在 init 里：config.json 是由进程启动流程装载
// 的，包 init 阶段读到的可能还是代码默认值；而 core/ja3 的指纹注册也发生在 init，
// 包之间的 init 顺序没有保证。
type rotator struct {
	once     sync.Once
	personas []*persona // 常备：一个出口一个，稳态只用这些
	reserves []*persona // 备用：只在常备全被挡住时顶上，见包注释
	buildErr error

	// allDirect 池子里一个代理都没有 —— 换身份换不掉 IP，第一次被挡时要说出来。
	//
	// ★ warned 是个受 mu 保护的 bool，不是 sync.Once：SetProxies 换了一批出口之后
	// 这句话值得重说一次，而"重置一个 sync.Once"就是往别的 goroutine 正在 Do 的
	// 那个对象上赋值 —— -race 下必炸的那种写法。
	allDirect bool
	warned    bool

	// idle 有身份放开位置时被通知一次（缓冲 1，非阻塞发）。等位置的 goroutine
	// 靠它立刻醒来，而不是干等一个轮询周期。build 里建；nil 也是安全的
	// （select 在 nil channel 上不会选中），只是退化成纯轮询。
	idle chan struct{}

	mu      sync.Mutex
	next    int
	nextRsv int
}

func (r *rotator) RoundTrip(req *http.Request) (*http.Response, error) {
	r.once.Do(r.build)
	if err := r.err(); err != nil {
		return nil, err
	}

	var (
		firstErr error
		// blocked 是最近一次被挡回的响应。全部身份都被挡时它就是要交还给调用方的
		// 那个 —— 在此之前每换一次身份都要把上一个关掉，否则每次被挡就漏一条连接。
		blocked *http.Response
	)
	release := func() {
		if blocked != nil {
			blocked.Body.Close()
			blocked = nil
		}
	}

	for attempt := 0; attempt < r.attempts(); attempt++ {
		// ctx 已经没了就不必再换身份重试 —— 换谁都来不及，而且会把一条"超时"
		// 掩盖成"三个代理都连不上"。
		if err := req.Context().Err(); err != nil {
			if blocked != nil {
				return blocked, nil // 已经拿到的答复比一句 ctx 超时有用
			}
			if firstErr != nil {
				return nil, firstErr
			}
			return nil, err
		}

		// 重发前把 body 倒回去。改的是**副本** —— RoundTripper 的契约是不得修改
		// 传进来的 Request，调用方（乃至 http.Client 的重定向逻辑）还持有它。
		attemptReq := req
		if attempt > 0 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				break
			}
			attemptReq = req.Clone(req.Context())
			attemptReq.Body = body
		}

		// 占住一条线路。等不到位置时 ctx 已经到期，与"对方不回话"是两件事，
		// acquire 的错误里说清了是哪一件。
		p, done, err := r.acquire(req.Context())
		if err != nil {
			if blocked != nil {
				return blocked, nil
			}
			if firstErr != nil {
				return nil, firstErr
			}
			return nil, err
		}

		rsp, err := p.roundTrip(attemptReq)

		switch {
		case err != nil:
			done()
			p.markFailure(cfg.Cooldown)
			if firstErr == nil {
				firstErr = err
			}
			log.Dbgf("出站身份 %s 失败（第 %d 次尝试）: %v", p.label, attempt+1, err)

		case cfg.rotatesOn(rsp.StatusCode):
			// 对方答了，答的是"不行"。这在 RoundTripper 眼里是一次成功的往返，
			// 不特殊处理的话这个身份会带着同一套指纹一遍遍撞上去。
			done()
			p.markBlocked(cfg.Cooldown, retryAfter(rsp, time.Now()))
			p.closeIdle()
			r.warnIfDirect(rsp.StatusCode)
			log.Dbgf("出站身份 %s 被挡回 HTTP %d（第 %d 次尝试），换一个身份", p.label, rsp.StatusCode, attempt+1)
			release()
			blocked = rsp

		default:
			p.markSuccess()
			release()
			// ★ 位置**留到响应体关闭**才放，不是拿到响应头就放。响应头回来时
			// 这条连接还在传 body：此刻放行下一个请求，h1 会另拨一条连接、h2 会
			// 开第二条流 —— 对方看到的仍然是同一个身份上的两个并发请求，闸门就
			// 白关了。兜底见 maxSlotHold。
			if rsp.Body != nil {
				rsp.Body = holdSlot(rsp.Body, done)
			} else {
				done()
			}
			return rsp, nil
		}

		if !replayable(req) {
			break
		}
	}

	if blocked != nil {
		return blocked, nil
	}
	if firstErr == nil {
		// 走到这里而一次都没试过，只可能是身份池是空的。**返回 (nil, nil) 会让
		// net/http 自己 panic**，那时候堆栈里已经没有本包了。
		firstErr = errors.New("core/outbound: 身份池是空的，一个请求都发不出去")
	}
	return nil, firstErr
}

// holdSlot 把"放开线路上的位置"绑到响应体的关闭上，并加一道超时兜底。
func holdSlot(body io.ReadCloser, done func()) io.ReadCloser {
	timer := time.AfterFunc(maxSlotHold, done)
	return &slotBody{
		ReadCloser: body,
		done: func() {
			timer.Stop()
			done()
		},
	}
}

type slotBody struct {
	io.ReadCloser
	done func()
}

// Close 先放位置再关 body：关 body 可能报错，而位置无论如何都得放开。
func (b *slotBody) Close() error {
	defer b.done()
	return b.ReadCloser.Close()
}

// attempts 一次请求最多试几个身份：身份不够就别虚张声势地重试同一个。
func (r *rotator) attempts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return min(maxAttempts, len(r.personas)+len(r.reserves))
}

// err 读建池错误。与 personas 一样受 mu 保护 —— setProxies 会清掉它。
func (r *rotator) err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buildErr
}

// replayable —— 换个身份重发这次请求安全吗？
//
// 语义上总是安全的：RoundTrip 返回错误意味着请求没被对方处理过，而被 403/429
// 挡回来意味着对方看了头就拒了，同样没有处理。问题只在**能不能**：Body 是个
// 一次性的 Reader，第二次拨号时它已经空了，重发会静默送出一个空请求 ——
// 除非 GetBody 在（http.NewRequest 对 bytes/strings/文件之外的 body 会自己填好
// 它），那就能重新要一份。
func replayable(req *http.Request) bool {
	return req.Body == nil || req.GetBody != nil
}

// retryAfter 读 Retry-After 头，读不到就回 0（用我们自己的退避）。
//
// 对方说的等待时间比我们猜的准：猜短了立刻再吃一次拒绝，猜长了白白停掉一个出口。
// 两种写法都认 —— 秒数（"120"）与 HTTP 日期。
//
// ★ 仍然受 cooldownMax 封顶（在 markBlocked 里）：一个出故障的或被劫持的响应
// 不该能把一个身份停掉一整天。对方真要我们等更久，下一次拒绝会让退避自己长上去。
func retryAfter(rsp *http.Response, now time.Time) time.Duration {
	v := strings.TrimSpace(rsp.Header.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// warnIfDirect —— 池子里一个代理都没有时，"换了身份"这件事换不掉 IP。
//
// 说一次就够（配置问题，不是事件），但必须说：一个已经在轮换指纹的功能没起作用，
// 而原因在配置里而不是代码里 —— 不说的话只能靠猜。
func (r *rotator) warnIfDirect(code int) {
	r.mu.Lock()
	if !r.allDirect || r.warned {
		r.mu.Unlock()
		return
	}
	r.warned = true
	r.mu.Unlock()

	log.Warnf("被对方挡回 HTTP %d 后换了身份，但**出口 IP 没变** —— "+
		"没有配 outbound.proxies 时能轮换的只有 TLS 指纹与 UA。"+
		"若对方是按 IP 限速，换多少个指纹都不会有效果。", code)
}

// acquire 选一个身份**并占住它在那条线路上的位置**，占不到就等。
//
// 等待是这个功能的全部意义：拿不到位置意味着这个身份上已经有一个请求在飞，
// 而"再从同一个身份发一个"正是要禁掉的事。等不到就让 ctx 说了算 —— 但错误里
// 必须说出等的是什么，否则调用方只看到一句 context deadline exceeded，
// 与"对方不回话"分不开。
func (r *rotator) acquire(ctx context.Context) (*persona, func(), error) {
	start := time.Now()
	for {
		if p, done := r.take(time.Now()); p != nil {
			return p, done, nil
		}
		select {
		case <-ctx.Done():
			return nil, nil, fmt.Errorf("core/outbound: 等了 %s 也没等到空闲的出站身份"+
				"（同一个身份上的请求是串行发出的，见 outbound 包注释）: %w",
				time.Since(start).Round(time.Millisecond), ctx.Err())
		case <-r.idle:
		case <-time.After(idlePoll):
		}
	}
}

// take 试一次：先在常备里轮转，跳过冷却中的与正忙的；常备取不到才动用备用。
// 取到时返回一个**只放一次**的释放函数；一个都取不到回 (nil, nil)。
//
// ★ 全部在冷却时**不返回 nil**，而是挑冷却最早到期的那个照样用。一个把 100% 出口
// 都摘掉的熔断器，比它要防的故障更糟（同 volts selector 里那条"全部熔断就全放回"）。
// 但这条 fail-open 只对**冷却**成立，不对**忙**成立：忙不是容量被摘掉，是"这条线路
// 上已经有一个请求"，等一下就有 —— 为它 fail-open 等于把闸门拆了。
func (r *rotator) take(now time.Time) (*persona, func()) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if p, next := takeFrom(r.personas, r.next, now); p != nil {
		r.next = next
		return p, r.releaser(p)
	}

	// ★ 常备里还有没在冷却的（只是忙）—— 等它，别动用备用。备用是"常备被挡住了"
	// 的例外，不是"常备正忙"的例外：为了一时的忙就把备用拉进来，稳态立刻退回成
	// "同一个 IP 上轮换多个指纹"，而那正是这个包论证过的、比不轮换更可疑的做法。
	if anyAvailable(r.personas, now) {
		return nil, nil
	}

	if p, next := takeFrom(r.reserves, r.nextRsv, now); p != nil {
		r.nextRsv = next
		return p, r.releaser(p)
	}
	if anyAvailable(r.reserves, now) {
		return nil, nil
	}

	// 全在冷却里：挑冷却最早到期、且还空着的那个。
	var soonest *persona
	for _, p := range r.all() {
		if p.busy() {
			continue
		}
		if soonest == nil || p.cooldownEnd().Before(soonest.cooldownEnd()) {
			soonest = p
		}
	}
	if soonest != nil && soonest.tryAcquire() {
		return soonest, r.releaser(soonest)
	}
	return nil, nil
}

// anyAvailable 这批里还有没有不在冷却里的（忙不忙不算）。**调用方须持有 r.mu。**
func anyAvailable(ps []*persona, now time.Time) bool {
	for _, p := range ps {
		if p.available(now) {
			return true
		}
	}
	return false
}

// takeFrom 从 start 开始占住第一个既不在冷却里、又还空着的身份。
func takeFrom(ps []*persona, start int, now time.Time) (*persona, int) {
	for i := 0; i < len(ps); i++ {
		p := ps[(start+i)%len(ps)]
		if p.available(now) && p.tryAcquire() {
			return p, (start + i + 1) % len(ps)
		}
	}
	return nil, start
}

// releaser 返回本次占位的释放函数。
//
// ★ 必须**每次占位一个**，不能做成 p.release() 那样的方法：闸门容量大于 1 时，
// 一个多放了一次的调用方会把别人的位置也放掉，于是并发悄悄变成 N+1。sync.Once
// 让重复调用（slotBody.Close 与 maxSlotHold 兜底会一起触发）是安全的。
func (r *rotator) releaser(p *persona) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			<-p.sem
			select {
			case r.idle <- struct{}{}:
			default: // 已经有一个未被取走的通知，够了
			}
		})
	}
}

// all 返回常备 + 备用。**调用方必须已持有 r.mu。**
func (r *rotator) all() []*persona {
	out := make([]*persona, 0, len(r.personas)+len(r.reserves))
	out = append(out, r.personas...)
	return append(out, r.reserves...)
}

func (r *rotator) build() {
	r.idle = make(chan struct{}, 1)

	active, reserve, err := buildPersonas(cfg)
	if err != nil {
		r.buildErr = err
		log.Errf("出站客户端不可用: %v", err)
		return
	}
	r.personas, r.reserves = active, reserve

	r.allDirect = allDirect(r.all())
	labels := make([]string, 0, len(active))
	for _, p := range active {
		labels = append(labels, p.label)
	}
	log.Dbgf("出站身份池 %d 个: %s（另有 %d 个备用身份，只在被挡回时启用）",
		len(active), strings.Join(labels, ", "), len(reserve))
}

// buildPersonas 把配置摊成常备身份与备用身份。
//
// 配对规则是**按下标轮着配**，不是笛卡尔积：3 个指纹 × 4 个代理得到 4 个身份，
// 不是 12 个。笛卡尔积的问题是同一个出口 IP 会以多个指纹出现 —— 那正是这个包
// 想避免的自相矛盾。
//
// UA 也跟着下标走：第 n 个身份用该指纹的第 n 条 UA（不同平台）。于是"两个身份
// 用同一个指纹但不同平台"是自然发生的，而不必额外配置。
//
// ★★ **常备身份数 = 出口数。**
//
// 这条反直觉，但想一遍就清楚：轮换的价值在于让对方**以为是不同的人**，而"不同的
// 人"首先意味着不同的 IP。同一个 IP 上出现 6 个浏览器身份，各自每 5 分钟查同一批
// 型号 —— 那比一个稳定的身份**更**像程序，因为真实世界里同一个出口后面的 6 个人
// 不会做一模一样的事。所以有几个代理就建几个身份，只有一个出口（直连）就一个。
//
// ★★ **剩下的指纹不丢，留作备用身份**（见包注释）。上面那条是稳态的规矩，而
// 一旦被 403/429 挡回来，"看起来自然"已经失败了，此时继续用一个确定不通的身份
// 才是唯一确定没有结果的选择。备用身份平时一次都不会被 pick 到。
//
// 显式配了 profiles 就全部是常备、没有备用 —— 那是"我就要这几个"的明确意图，
// 不该被单出口规则吃掉，也不该有它没点名的身份在背后顶上。
func buildPersonas(c *Config) (active, reserve []*persona, err error) {
	fps := resolveFingerprints(c)
	proxies, err := resolveProxies(c)
	if err != nil {
		return nil, nil, err
	}

	active, reserve = assemble(fps, proxies, len(c.Profiles) > 0, c.slots())
	return active, reserve, nil
}

// assemble 是上面那套配对规则本身，与配置解析分开只为能拿假指纹直接验它。
//
// explicit = 调用方显式点名了 profiles；slots = 每条线路的并发闸门容量。
func assemble(fps []*transport.Fingerprint, proxies []*proxyEndpoint, explicit bool, slots int) (active, reserve []*persona) {
	n := len(proxies)
	if explicit && len(fps) > n {
		n = len(fps)
	}
	for i := 0; i < n; i++ {
		active = append(active, newPersona(fps[i%len(fps)], i, proxies[i%len(proxies)], slots))
	}
	for i := n; i < len(fps); i++ {
		reserve = append(reserve, newPersona(fps[i], i, proxies[i%len(proxies)], slots))
	}
	return active, reserve
}

// allDirect 池子里一个代理都没有 —— 换身份换不掉 IP。
func allDirect(ps []*persona) bool {
	for _, p := range ps {
		if p.out != nil {
			return false
		}
	}
	return true
}

// resolveFingerprints 返回要用的指纹，**至少一个元素**（nil 表示"不伪装"）。
func resolveFingerprints(c *Config) []*transport.Fingerprint {
	available := transport.Fingerprints()
	if len(available) == 0 {
		// 没有空导入 core/ja3。**说出来** —— 伪装失效是静默的，表现只是
		// "请求照常发出去了"，没有任何地方会提示身份其实是 Go 的默认值。
		log.Warnf("没有可用的 TLS 指纹：出站将带着 Go 的默认指纹与 User-Agent。" +
			`要启用伪装，在本服务的 main 里空导入 _ "github.com/volts-dev/volts/core/ja3"`)
		return []*transport.Fingerprint{nil}
	}

	if len(c.Profiles) == 0 {
		return preferDefault(available) // 留空 = 全用上，见 Config.Profiles 的注释
	}

	var out []*transport.Fingerprint
	for _, name := range c.Profiles {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		fp, ok := transport.LookupFingerprint(name)
		if !ok {
			// 坏的那条跳过、其余照用：池里每条指纹是等价的，少一条不改变语义。
			// 但必须留声并**列出可选项** —— 静默跳过的话，"我配了 3 个指纹"与
			// "实际在用 1 个"之间没有任何可见的差别。
			log.Warnf("outbound.profiles 里的 %q 不是已知指纹，已跳过；可用的有 %s",
				name, strings.Join(transport.FingerprintNames(), ", "))
			continue
		}
		out = append(out, fp)
	}

	if len(out) == 0 {
		log.Warnf("配置的 %d 个指纹一个都不认识，退回全部可用指纹", len(c.Profiles))
		return available
	}
	return out
}

// defaultProfile 是"没说要哪个"时排在最前的那个身份。
//
// 挑最新的主流 Chrome，因为单出口时只会用到**第一个**（见 buildPersonas）——
// 而 transport.Fingerprints() 是按名字排序的，那会让 chrome_120 这个老版本
// 排在前面。桌面份额最大的那个才是最不显眼的选择。
const defaultProfile = "chrome_133"

// preferDefault 把 defaultProfile 挪到最前，其余保持原有的稳定顺序。
func preferDefault(fps []*transport.Fingerprint) []*transport.Fingerprint {
	for i, fp := range fps {
		if fp != nil && fp.Name == defaultProfile {
			out := make([]*transport.Fingerprint, 0, len(fps))
			out = append(out, fp)
			out = append(out, fps[:i]...)
			return append(out, fps[i+1:]...)
		}
	}
	return fps
}

// runtimeProxies 是 SetProxies 落下的运行时覆盖；nil = 没有覆盖，用配置文件的。
var runtimeProxies atomic.Pointer[[]string]

// effectiveProxies 返回这一轮建池要用的代理列表，以及它是不是来自运行时覆盖。
func effectiveProxies(c *Config) []string {
	if p := runtimeProxies.Load(); p != nil {
		return *p
	}
	return c.Proxies
}

// resolveProxies 建代理拨号器，**至少一个元素**（nil 表示直连）。
func resolveProxies(c *Config) ([]*proxyEndpoint, error) {
	configured := effectiveProxies(c)
	var out []*proxyEndpoint
	for _, raw := range configured {
		u := strings.TrimSpace(raw)
		if u == "" {
			continue
		}
		dialer, err := transport.NewProxyDialer(u, "")
		if err != nil {
			// 同指纹：坏的跳过、其余照用。但**不能**在这里悄悄补一个直连 ——
			// 那是 allow_direct 才能决定的事。
			log.Warnf("代理 %s 建不起来，已跳过: %v", RedactProxy(u), err)
			continue
		}
		out = append(out, &proxyEndpoint{url: u, dialer: dialer})
	}

	if len(out) == 0 {
		if len(configured) > 0 && !c.AllowDirect {
			return nil, ErrNoUsableProxy
		}
		if len(configured) > 0 {
			log.Warnf("配置的 %d 个代理一个都不可用，按 allow_direct=true 退回直连 —— 出站会带上本机的真实 IP", len(configured))
		}
		out = append(out, nil) // 直连
	}
	return out, nil
}

// RedactProxy 去掉代理 URL 里的密码再印。日志会进文件、进工单、进截图。
//
// ★★ ss/ssr **整条都不能印**。它们的凭据不在 `@` 前面 —— 标准写法是
// `ss://<一大段 base64>#节点名`，那段 base64 里就是 `method:password@host:port`。
// 按 `@` 打码的老写法对它完全无效：`@` 找不到就原样返回，于是口令进了日志。
// 这里只保留能对人有用的部分：协议 + 节点名（`#` 后面那段是机场起的名字，
// 不是凭据），主机名也一并隐去 —— 它同样是这条订阅的身份。
func RedactProxy(raw string) string {
	if scheme, rest, ok := strings.Cut(raw, "://"); ok {
		switch strings.ToLower(scheme) {
		case "ss", "ssr":
			if _, tag, found := strings.Cut(rest, "#"); found && tag != "" {
				// 节点名是 URL 转义过的（机场爱用 emoji 和空格）。这一行是这个
				// 功能最主要的诊断出口，印一串 %F0%9F%87%AD 等于没印。
				if un, err := url.QueryUnescape(tag); err == nil && un != "" {
					tag = un
				}
				return scheme + "://***#" + tag
			}
			return scheme + "://***"
		}
	}
	at := strings.LastIndex(raw, "@")
	if at < 0 {
		return raw
	}
	scheme := ""
	if i := strings.Index(raw, "://"); i >= 0 {
		scheme = raw[:i+3]
	}
	return scheme + "***@" + raw[at+1:]
}
