package net

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// HostPort format addr and port suitable for dial
func HostPort(addr string, port interface{}) string {
	host := addr
	if strings.Count(addr, ":") > 0 {
		host = fmt.Sprintf("[%s]", addr)
	}
	// when port is blank or 0, host is a queue name
	if v, ok := port.(string); ok && v == "" {
		return host
	} else if v, ok := port.(int); ok && v == 0 && net.ParseIP(host) == nil {
		return host
	}

	return fmt.Sprintf("%s:%v", host, port)
}

// normalizeBindAddr 将纯回环监听地址转换为全接口监听地址。
// localhost:PORT / 127.0.0.1:PORT / ::1:PORT → :PORT
// 其他地址原样返回。
func normalizeBindAddr(addr string) string {
	// 处理 ::1:PORT 格式（net.SplitHostPort 无法直接解析）
	if strings.HasPrefix(addr, "::1:") {
		port := strings.TrimPrefix(addr, "::1:")
		// 确保 port 部分不含冒号（避免误匹配）
		if !strings.Contains(port, ":") {
			return ":" + port
		}
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return ":" + port
	}
	return addr
}

// Listen takes addr:portmin-portmax and binds to the first available port
// Example: Listen("localhost:5000-6000", fn)
func Listen(addr string, fn func(string) (net.Listener, error)) (net.Listener, error) {
	addr = normalizeBindAddr(addr) // 将回环地址扩展为全接口绑定

	if strings.Count(addr, ":") == 1 && strings.Count(addr, "-") == 0 {
		return fn(addr)
	}

	// host:port || host:min-max
	host, ports, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}

	// try to extract port range
	prange := strings.Split(ports, "-")

	// single port
	if len(prange) < 2 {
		return fn(addr)
	}

	// we have a port range

	// extract min port
	min, err := strconv.Atoi(prange[0])
	if err != nil {
		return nil, errors.New("unable to extract port range")
	}

	// extract max port
	max, err := strconv.Atoi(prange[1])
	if err != nil {
		return nil, errors.New("unable to extract port range")
	}

	// range the ports
	for port := min; port <= max; port++ {
		// try bind to host:port
		ln, err := fn(HostPort(host, port))
		if err == nil {
			return ln, nil
		}

		// hit max port
		if port == max {
			return nil, err
		}
	}

	// why are we here?
	return nil, fmt.Errorf("unable to bind to %s", addr)
}

// Proxy returns the proxy and the address if it exits
func Proxy(service string, address []string) (string, []string, bool) {
	var hasProxy bool

	// get proxy. we parse out address if present
	if prx := os.Getenv("VOLTS_PROXY"); len(prx) > 0 {
		// default name
		if prx == "service" {
			prx = "go.volts.proxy"
			address = nil
		}

		// check if its an address
		if v := strings.Split(prx, ":"); len(v) > 1 {
			address = []string{prx}
		}

		service = prx
		hasProxy = true

		return service, address, hasProxy
	}

	if prx := os.Getenv("VOLTS_NETWORK"); len(prx) > 0 {
		// default name
		if prx == "service" {
			prx = "go.volts.network"
		}
		service = prx
		hasProxy = true
	}

	if prx := os.Getenv("VOLTS_NETWORK_ADDRESS"); len(prx) > 0 {
		address = []string{prx}
		hasProxy = true
	}

	return service, address, hasProxy
}
