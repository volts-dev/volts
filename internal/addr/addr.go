package addr

import (
	"fmt"
	"net"
	"sync"
	"time"
)

var (
	privateBlocks []*net.IPNet

	// 缓存本机 IP 列表，避免每次 RPC 调用都执行 net.Interfaces() 系统调用
	localIPMu       sync.RWMutex
	localIPStrings  map[string]bool // IP string -> true
	localIPCacheAt  time.Time
	localIPCacheTTL = 30 * time.Second
)

func init() {
	for _, b := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "fd00::/8"} {
		if _, block, err := net.ParseCIDR(b); err == nil {
			privateBlocks = append(privateBlocks, block)
		}
	}
}

// AppendPrivateBlocks append private network blocks
func AppendPrivateBlocks(bs ...string) {
	for _, b := range bs {
		if _, block, err := net.ParseCIDR(b); err == nil {
			privateBlocks = append(privateBlocks, block)
		}
	}
}

func isPrivateIP(ipAddr string) bool {
	ip := net.ParseIP(ipAddr)
	for _, priv := range privateBlocks {
		if priv.Contains(ip) {
			return true
		}
	}
	return false
}

// cachedLocalIPs 返回缓存的本机 IP 字符串集合
// 使用 30s TTL 缓存，避免频繁系统调用
func cachedLocalIPs() map[string]bool {
	localIPMu.RLock()
	if localIPStrings != nil && time.Since(localIPCacheAt) < localIPCacheTTL {
		ips := localIPStrings
		localIPMu.RUnlock()
		return ips
	}
	localIPMu.RUnlock()

	localIPMu.Lock()
	defer localIPMu.Unlock()

	// double check
	if localIPStrings != nil && time.Since(localIPCacheAt) < localIPCacheTTL {
		return localIPStrings
	}

	ips := IPs()
	m := make(map[string]bool, len(ips))
	for _, ip := range ips {
		m[ip.String()] = true
	}
	localIPStrings = m
	localIPCacheAt = time.Now()
	return m
}

// LocalFormat 将单个地址中的本机 IP 转换为 loopback 地址以获得最低延迟
// 高性能版本：使用缓存的 IP 查找，无 slice 分配
func LocalFormat(address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}

	// 快速路径：已经是 loopback
	if host == "127.0.0.1" || host == "::1" {
		return address
	}

	if host == "localhost" {
		return net.JoinHostPort("127.0.0.1", port)
	}

	// 通配符地址
	if host == "0.0.0.0" || host == "[::]" || host == "::" {
		return net.JoinHostPort("127.0.0.1", port)
	}

	// 检查是否是本机 IP（使用缓存）
	if cachedLocalIPs()[host] {
		if ip := net.ParseIP(host); ip != nil {
			if ip.To4() != nil {
				return net.JoinHostPort("127.0.0.1", port)
			}
			return net.JoinHostPort("::1", port)
		}
	}

	return address
}

// LocalFormater 批量版本，将地址列表中的本机 IP 转换为 loopback
func LocalFormater(addrs ...string) []string {
	for idx := range addrs {
		addrs[idx] = LocalFormat(addrs[idx])
	}

	return addrs
}

// IsLocal tells us whether an ip is local
func IsLocal(addr string) bool {
	// extract the host
	host, _, err := net.SplitHostPort(addr)
	if err == nil {
		addr = host
	}

	// check if its localhost
	if addr == "localhost" {
		return true
	}

	// check against all local ips
	for _, ip := range IPs() {
		if addr == ip.String() {
			return true
		}
	}

	return false
}

// Extract returns a real ip
func Extract(addr string) (string, error) {
	// if addr specified then its returned
	if len(addr) > 0 && (addr != "0.0.0.0" && addr != "[::]" && addr != "::") {
		return addr, nil
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("Failed to get interfaces! Err: %v", err)
	}

	//nolint:prealloc
	var addrs []net.Addr
	var loAddrs []net.Addr
	for _, iface := range ifaces {
		ifaceAddrs, err := iface.Addrs()
		if err != nil {
			// ignore error, interface can disappear from system
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 {
			loAddrs = append(loAddrs, ifaceAddrs...)
			continue
		}
		addrs = append(addrs, ifaceAddrs...)
	}
	addrs = append(addrs, loAddrs...)

	var ipAddr string
	var publicIP string

	for _, rawAddr := range addrs {
		var ip net.IP
		switch addr := rawAddr.(type) {
		case *net.IPAddr:
			ip = addr.IP
		case *net.IPNet:
			ip = addr.IP
		default:
			continue
		}

		if !isPrivateIP(ip.String()) {
			publicIP = ip.String()
			continue
		}

		ipAddr = ip.String()
		break
	}

	// return private ip
	if len(ipAddr) > 0 {
		a := net.ParseIP(ipAddr)
		if a == nil {
			return "", fmt.Errorf("ip addr %s is invalid", ipAddr)
		}
		return a.String(), nil
	}

	// return public or virtual ip
	if len(publicIP) > 0 {
		a := net.ParseIP(publicIP)
		if a == nil {
			return "", fmt.Errorf("ip addr %s is invalid", publicIP)
		}
		return a.String(), nil
	}

	return "", fmt.Errorf("No IP address found, and explicit IP not provided")
}

// IPs returns all known ips
func IPs() []net.IP {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	var ipAddrs []net.IP

	for _, i := range ifaces {
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}

			if ip == nil {
				continue
			}

			// dont skip ipv6 addrs
			/*
				ip = ip.To4()
				if ip == nil {
					continue
				}
			*/

			ipAddrs = append(ipAddrs, ip)
		}
	}

	return ipAddrs
}
