package memory

import "golang.org/x/sys/unix"

// readHost 可用量与 Linux MemAvailable 同义：空闲 + 预读 + 可清除 + 文件页（可直接丢的
// 外部页）。计数单位是内核页（Apple 芯片上 16K），所以乘 hw.pagesize 而不是 4096。
// 与 vectors core/nodestat 同一口径。
func readHost() (Host, bool) {
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil || total == 0 {
		return Host{}, false
	}
	page, err := unix.SysctlUint32("hw.pagesize")
	if err != nil || page == 0 {
		return Host{}, false
	}
	var pages uint64
	for _, name := range []string{
		"vm.page_free_count", "vm.page_speculative_count",
		"vm.page_purgeable_count", "vm.page_pageable_external_count",
	} {
		n, err := unix.SysctlUint32(name)
		if err != nil {
			return Host{}, false
		}
		pages += uint64(n)
	}
	avail := pages * uint64(page)
	if avail > total {
		avail = total
	}
	return Host{Total: total, Available: avail}, true
}
