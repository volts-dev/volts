package memlimit

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindBudget_V2_OwnTakesSmallestOfHighAndMax(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "sys/fs/cgroup/cgroup.controllers", "cpu memory\n")
	writeFile(t, root, "sys/fs/cgroup/system.slice/kylin.service/memory.max", "1800000000\n")
	writeFile(t, root, "sys/fs/cgroup/system.slice/kylin.service/memory.high", "1500000000\n")

	b := FindBudget(root, []byte("0::/system.slice/kylin.service\n"))
	if b.Own != 1500000000 || b.Shared != 0 {
		t.Fatalf("got %+v, want Own=1500000000 and no shared", b)
	}
}

func TestFindBudget_V2_SliceLimitIsShared(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "sys/fs/cgroup/cgroup.controllers", "memory\n")
	writeFile(t, root, "sys/fs/cgroup/vs.slice/memory.high", "2600000000\n")
	writeFile(t, root, "sys/fs/cgroup/vs.slice/memory.max", "3000000000\n")
	writeFile(t, root, "sys/fs/cgroup/vs.slice/websvc.service/memory.max", "max\n")
	writeFile(t, root, "sys/fs/cgroup/vs.slice/websvc.service/memory.high", "max\n")

	b := FindBudget(root, []byte("0::/vs.slice/websvc.service\n"))
	if b.Own != 0 || b.Shared != 2600000000 || b.SharedName != "/vs.slice" {
		t.Fatalf("got %+v, want Shared=2600000000 at /vs.slice", b)
	}
}

func TestFindBudget_V2_NearestAncestorWins(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "sys/fs/cgroup/cgroup.controllers", "memory\n")
	writeFile(t, root, "sys/fs/cgroup/a.slice/memory.max", "1000000000\n")
	writeFile(t, root, "sys/fs/cgroup/a.slice/a-b.slice/memory.max", "4000000000\n")

	b := FindBudget(root, []byte("0::/a.slice/a-b.slice/x.service\n"))
	if b.Shared != 4000000000 {
		t.Fatalf("got %+v, want the nearest ancestor (4000000000)", b)
	}
}

func TestFindBudget_V2_NoLimit(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "sys/fs/cgroup/cgroup.controllers", "memory\n")
	writeFile(t, root, "sys/fs/cgroup/system.slice/kylin.service/memory.max", "max\n")

	if b := FindBudget(root, []byte("0::/system.slice/kylin.service\n")); b != (Budget{}) {
		t.Fatalf("got %+v, want empty", b)
	}
}

func TestFindBudget_V1(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "sys/fs/cgroup/memory/docker/abc/memory.limit_in_bytes", "1073741824\n")
	self := "12:cpu,cpuacct:/docker/abc\n4:memory:/docker/abc\n"

	if b := FindBudget(root, []byte(self)); b.Own != 1073741824 {
		t.Fatalf("got %+v, want Own=1073741824", b)
	}
}

func TestFindBudget_V1_ContainerViewFallsBackToMountRoot(t *testing.T) {
	root := t.TempDir()
	// 容器内只挂了自己那一层：宿主机路径下没有文件。
	writeFile(t, root, "sys/fs/cgroup/memory/memory.limit_in_bytes", "536870912\n")

	if b := FindBudget(root, []byte("4:memory:/docker/abc\n")); b.Own != 536870912 {
		t.Fatalf("got %+v, want Own=536870912", b)
	}
}

func TestFindBudget_V1_Unlimited(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "sys/fs/cgroup/memory/memory.limit_in_bytes", "9223372036854771712\n")

	if b := FindBudget(root, []byte("4:memory:/\n")); b.Own != 0 {
		t.Fatalf("got %+v, want 0", b)
	}
}

func TestFindBudget_NoCgroupFs(t *testing.T) {
	if b := FindBudget(t.TempDir(), nil); b != (Budget{}) {
		t.Fatalf("got %+v, want empty", b)
	}
}

const mib = 1 << 20

// 三个进程共用一个预算：各自按比例算出的软上限加起来正好是 预算×Ratio。
func TestSharedLimit_SharesSumToTarget(t *testing.T) {
	const budget = 2600 * mib
	mine := []uint64{600 * mib, 400 * mib, 300 * mib}
	var used, sum uint64
	for _, m := range mine {
		used += m
	}
	for _, m := range mine {
		sum += uint64(SharedLimit(budget, used, m))
	}
	target := uint64(budget * Ratio)
	if d := int64(sum) - int64(target); d > 4 || d < -4 {
		t.Fatalf("sum of limits %d, want %d", sum, target)
	}
}

// 一个进程冲高之后，别的进程的软上限跟着变小。
func TestSharedLimit_OthersTightenWhenOneSpikes(t *testing.T) {
	const budget = 2600 * mib
	before := SharedLimit(budget, 1300*mib, 400*mib)
	after := SharedLimit(budget, 2300*mib, 400*mib) // 另一个进程多拿了 1G
	if after >= before {
		t.Fatalf("limit before spike %d, after %d — want it to shrink", before, after)
	}
}

// 已经超了预算：软上限低于自己现用，GC 立刻收紧；但不低于 MinLimit。
func TestSharedLimit_OverBudget(t *testing.T) {
	const budget = 1000 * mib
	if got := SharedLimit(budget, 1200*mib, 600*mib); got >= 600*mib {
		t.Fatalf("got %d, want below current usage", got)
	}
	if got := SharedLimit(budget, 5000*mib, 10*mib); got != MinLimit {
		t.Fatalf("got %d, want MinLimit", got)
	}
}

// 读不到用量时退回整份目标（等于独占）。
func TestSharedLimit_UnknownUsage(t *testing.T) {
	if got := SharedLimit(1000*mib, 0, 100*mib); got != int64(1000*mib*Ratio) {
		t.Fatalf("got %d", got)
	}
}

func TestBudgetLimit_OwnCapsShared(t *testing.T) {
	b := Budget{Own: 500 * mib, Shared: 2600 * mib}
	if got := b.limit(100*mib, 300*mib); got != int64(500*mib*Ratio) {
		t.Fatalf("got %d, want own cap %d", got, int64(500*mib*Ratio))
	}
}

func TestSharedUsage_SubtractsInactiveFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "memory.current", "2000000000\n")
	writeFile(t, dir, "memory.stat", "anon 900000000\nfile 1100000000\nactive_file 300000000\ninactive_file 800000000\n")
	if got := sharedUsage(dir); got != 1200000000 {
		t.Fatalf("got %d, want 1200000000", got)
	}
}
