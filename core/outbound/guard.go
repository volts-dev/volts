package outbound

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"
)

// ErrNonPublicAddress —— 请求标了 WithPublicOnly，而目标（或它解析出的某个地址）不是公网地址。
var ErrNonPublicAddress = errors.New("outbound: refusing to connect to a non-public address")

type publicOnlyKey struct{}

// WithPublicOnly 让用这个 ctx 发出的请求**只许连公网地址**。
//
// 给谁用：目标地址不是自己写死、而是外来的那一类 —— 典型是 AI 工具「抓这个网页」，
// 网址来自大模型，而大模型的输入可以被网页内容操纵（提示注入）。不挡的话，一句话就能让
// 服务器去读本机的 etcd（localhost:2379）、内网的管理端口，或云主机的元数据接口
// （阿里云 100.100.100.200、AWS/GCP 169.254.169.254）。
//
// 两道闸，缺一不可：
//
//  1. **每一跳请求进来时**（RoundTrip 开头）先把主机名解析一遍，任何一个地址不是公网就拒。
//     重定向的每一跳都重新走 RoundTrip，所以「先给公网地址、再 302 到 127.0.0.1」挡得住；
//     连接池里**别的请求**留下的、通往内网的空闲连接也因此碰不到 —— 池按 host:port 复用，
//     只在拨号时检查的话，复用的那条连接根本不经过拨号。
//  2. **直连拨号的那一刻**（net.Dialer.Control）再查一次实际要连的 IP。第一道查的是那一刻的
//     DNS 答案，拨号时会再解析一次；域名的 TTL 设成 0、第一次答公网第二次答 127.0.0.1
//     （DNS rebinding）就能从两次解析的缝里穿过去，只有拨号时查的才是真正连上的那个地址。
//
// ★ 走代理的身份只有第一道：地址由代理去解析、去连，这边看不到。代理若在本机且以 fake-IP
// 模式工作（Clash 一类：任何域名都先解析成 198.18.x.x），一个解析到 127.0.0.1 的恶意域名
// 会被代理连到**代理所在机器**的环回上。生产机不该这样配；开发机上知道有这道缝就好。
//
// ★ 198.18.0.0/15 与 2001:2::/48（基准测试保留段）**不拦**：fake-IP 代理正是用它们给每个域名
// 发假地址，拦了就是开发机上一个网页都抓不了。公网上不会有真服务器落在这两段里。
func WithPublicOnly(ctx context.Context) context.Context {
	return context.WithValue(ctx, publicOnlyKey{}, true)
}

func publicOnly(ctx context.Context) bool {
	v, _ := ctx.Value(publicOnlyKey{}).(bool)
	return v
}

// checkPublicHost 是第一道闸：把主机名解析一遍，所有地址都得是公网的。
func checkPublicHost(ctx context.Context, host string) error {
	if host == "" {
		return fmt.Errorf("%w: empty host", ErrNonPublicAddress)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if !isPublicAddr(ip) {
			return fmt.Errorf("%w: %s", ErrNonPublicAddress, host)
		}
		return nil
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return err
	}
	for _, a := range addrs {
		if !isPublicAddr(a) {
			// 只要有一个不是就拒：拨号时选哪一个由系统决定，不由这里决定。
			return fmt.Errorf("%w: %s resolves to %s", ErrNonPublicAddress, host, a)
		}
	}
	return nil
}

// publicDialControl 是第二道闸：net.Dialer.Control 拿到的 address 已经是解析完的 ip:port。
func publicDialControl(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrNonPublicAddress, address)
	}
	if !isPublicAddr(ap.Addr()) {
		return fmt.Errorf("%w: %s", ErrNonPublicAddress, address)
	}
	return nil
}

// 标准库的 IsPrivate / IsLoopback 之外还要拦的段。
var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),     // 「本网络」，Linux 上连 0.x 等于连本机
	netip.MustParsePrefix("100.64.0.0/10"), // 运营商级 NAT；阿里云的元数据接口 100.100.100.200 就在这里
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF 协议分配
	netip.MustParsePrefix("240.0.0.0/4"),   // 保留（含 255.255.255.255 广播）
}

// nat64Prefix —— 64:ff9b::/96 把一个 IPv4 地址嵌在最后 32 位里，要按里面那个判。
var nat64Prefix = netip.MustParsePrefix("64:ff9b::/96")

func isPublicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if nat64Prefix.Contains(ip) {
		b := ip.As16()
		return isPublicAddr(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
	}
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() {
		return false
	}
	for _, p := range nonPublicPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
