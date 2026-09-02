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
// # 配置
//
//	"outbound": {
//	  "profiles": ["chrome_133", "firefox_120"],
//	  "proxies": ["socks5://127.0.0.1:1086", "http://user:${PROXY_PASS}@gw:8080"],
//	  "allow_direct": false,
//	  "cooldown": 30000
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
	"errors"
	"net/http"
	"strings"
	"sync"
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
		config.Config `field:"-"`
		prefix        string `field:"-"`

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
	}
)

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
)

var cfg = &Config{
	prefix:      "outbound",
	AllowDirect: false,
	Cooldown:    30 * time.Second,
}

func init() { config.Register(cfg) }

func (c *Config) String() string { return c.prefix }

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
	if r.buildErr != nil {
		return []string{"出站不可用: " + r.buildErr.Error()}
	}
	out := make([]string, 0, len(r.personas))
	for _, p := range r.personas {
		out = append(out, p.label)
	}
	return out
}

// rotator 是按身份轮换的 RoundTripper。
//
// 池子**惰性构建**（首次请求时），不在 init 里：config.json 是由进程启动流程装载
// 的，包 init 阶段读到的可能还是代码默认值；而 core/ja3 的指纹注册也发生在 init，
// 包之间的 init 顺序没有保证。
type rotator struct {
	once     sync.Once
	personas []*persona
	buildErr error

	mu   sync.Mutex
	next int
}

func (r *rotator) RoundTrip(req *http.Request) (*http.Response, error) {
	r.once.Do(r.build)
	if r.buildErr != nil {
		return nil, r.buildErr
	}

	var firstErr error
	for attempt := 0; attempt < min(maxAttempts, len(r.personas)); attempt++ {
		// ctx 已经没了就不必再换身份重试 —— 换谁都来不及，而且会把一条"超时"
		// 掩盖成"三个代理都连不上"。
		if err := req.Context().Err(); err != nil {
			if firstErr != nil {
				return nil, firstErr
			}
			return nil, err
		}

		p := r.pick()
		rsp, err := p.roundTrip(req)
		if err == nil {
			p.markSuccess()
			return rsp, nil
		}
		p.markFailure(cfg.Cooldown)
		if firstErr == nil {
			firstErr = err
		}
		log.Dbgf("出站身份 %s 失败（第 %d 次尝试）: %v", p.label, attempt+1, err)

		// RoundTrip 拿到 error 就意味着**没有**响应，请求没被对方处理过，重放是
		// 安全的 —— 但只在没有 body 的情况下：body 是个只能读一次的 Reader，
		// 第二次拨号时它已经空了，重放会静默发出一个空请求。
		if req.Body != nil || req.GetBody != nil {
			break
		}
	}
	return nil, firstErr
}

// pick 取下一个身份：轮转，跳过冷却中的。
//
// ★ 全部在冷却时**不返回 nil**，而是挑冷却最早到期的那个照样用。一个把 100% 出口
// 都摘掉的熔断器，比它要防的故障更糟（同 volts selector 里那条"全部熔断就全放回"）。
func (r *rotator) pick() *persona {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	var soonest *persona
	for i := 0; i < len(r.personas); i++ {
		p := r.personas[(r.next+i)%len(r.personas)]
		if p.available(now) {
			r.next = (r.next + i + 1) % len(r.personas)
			return p
		}
		if soonest == nil || p.cooldownEnd().Before(soonest.cooldownEnd()) {
			soonest = p
		}
	}
	r.next = (r.next + 1) % len(r.personas)
	return soonest
}

func (r *rotator) build() {
	personas, err := buildPersonas(cfg)
	if err != nil {
		r.buildErr = err
		log.Errf("出站客户端不可用: %v", err)
		return
	}
	r.personas = personas

	labels := make([]string, 0, len(personas))
	for _, p := range personas {
		labels = append(labels, p.label)
	}
	log.Dbgf("出站身份池 %d 个: %s", len(personas), strings.Join(labels, ", "))
}

// buildPersonas 把配置摊成一组身份。
//
// 配对规则是**按下标轮着配**，不是笛卡尔积：3 个指纹 × 4 个代理得到 4 个身份，
// 不是 12 个。笛卡尔积的问题是同一个出口 IP 会以多个指纹出现 —— 那正是这个包
// 想避免的自相矛盾。
//
// UA 也跟着下标走：第 n 个身份用该指纹的第 n 条 UA（不同平台）。于是"两个身份
// 用同一个指纹但不同平台"是自然发生的，而不必额外配置。
//
// ★★ **没有代理时默认只建一个身份。**
//
// 这条反直觉，但想一遍就清楚：轮换的价值在于让对方**以为是不同的人**，而"不同的
// 人"首先意味着不同的 IP。同一个 IP 上出现 6 个浏览器身份，各自每 5 分钟查同一批
// 型号 —— 那比一个稳定的身份**更**像程序，因为真实世界里同一个出口后面的 6 个人
// 不会做一模一样的事。
//
// 所以：有几个代理就建几个身份（一个出口一个身份，各自自洽）；只有一个出口
// （直连）就一个身份。要在单 IP 上强行轮换指纹，显式配 profiles 即可 —— 那是
// 一个需要说明理由的选择，不该是默认。
func buildPersonas(c *Config) ([]*persona, error) {
	fps := resolveFingerprints(c)
	proxies, err := resolveProxies(c)
	if err != nil {
		return nil, err
	}

	// 单出口 + 没有显式点名指纹 → 只留一个身份，理由见上。
	if len(proxies) == 1 && proxies[0] == nil && len(c.Profiles) == 0 && len(fps) > 1 {
		fps = fps[:1]
	}

	n := max(len(fps), len(proxies))
	out := make([]*persona, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, newPersona(fps[i%len(fps)], i, proxies[i%len(proxies)]))
	}
	return out, nil
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

// resolveProxies 建代理拨号器，**至少一个元素**（nil 表示直连）。
func resolveProxies(c *Config) ([]*proxyEndpoint, error) {
	var out []*proxyEndpoint
	for _, raw := range c.Proxies {
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
		if len(c.Proxies) > 0 && !c.AllowDirect {
			return nil, ErrNoUsableProxy
		}
		if len(c.Proxies) > 0 {
			log.Warnf("配置的 %d 个代理一个都不可用，按 allow_direct=true 退回直连 —— 出站会带上本机的真实 IP", len(c.Proxies))
		}
		out = append(out, nil) // 直连
	}
	return out, nil
}

// RedactProxy 去掉代理 URL 里的密码再印。日志会进文件、进工单、进截图。
func RedactProxy(raw string) string {
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
