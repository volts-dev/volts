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

func TestFromCgroup_V2_TakesSmallestOfHighAndMaxUpTheTree(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "sys/fs/cgroup/cgroup.controllers", "cpu memory\n")
	writeFile(t, root, "sys/fs/cgroup/system.slice/memory.max", "3000000000\n")
	writeFile(t, root, "sys/fs/cgroup/system.slice/kylin.service/memory.max", "1800000000\n")
	writeFile(t, root, "sys/fs/cgroup/system.slice/kylin.service/memory.high", "1500000000\n")

	got := FromCgroup(root, []byte("0::/system.slice/kylin.service\n"))
	if got != 1500000000 {
		t.Fatalf("got %d, want memory.high of the unit (1500000000)", got)
	}
}

func TestFromCgroup_V2_ParentSliceLimitApplies(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "sys/fs/cgroup/cgroup.controllers", "memory\n")
	writeFile(t, root, "sys/fs/cgroup/vs.slice/memory.max", "2000000000\n")
	writeFile(t, root, "sys/fs/cgroup/vs.slice/websvc.service/memory.max", "max\n")
	writeFile(t, root, "sys/fs/cgroup/vs.slice/websvc.service/memory.high", "max\n")

	if got := FromCgroup(root, []byte("0::/vs.slice/websvc.service\n")); got != 2000000000 {
		t.Fatalf("got %d, want the slice limit 2000000000", got)
	}
}

func TestFromCgroup_V2_NoLimitIsZero(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "sys/fs/cgroup/cgroup.controllers", "memory\n")
	writeFile(t, root, "sys/fs/cgroup/system.slice/kylin.service/memory.max", "max\n")

	if got := FromCgroup(root, []byte("0::/system.slice/kylin.service\n")); got != 0 {
		t.Fatalf("got %d, want 0 (unlimited)", got)
	}
}

func TestFromCgroup_V1(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "sys/fs/cgroup/memory/docker/abc/memory.limit_in_bytes", "1073741824\n")
	self := "12:cpu,cpuacct:/docker/abc\n4:memory:/docker/abc\n"

	if got := FromCgroup(root, []byte(self)); got != 1073741824 {
		t.Fatalf("got %d, want 1073741824", got)
	}
}

func TestFromCgroup_V1_ContainerViewFallsBackToMountRoot(t *testing.T) {
	root := t.TempDir()
	// 容器内只挂了自己那一层：宿主机路径下没有文件。
	writeFile(t, root, "sys/fs/cgroup/memory/memory.limit_in_bytes", "536870912\n")

	if got := FromCgroup(root, []byte("4:memory:/docker/abc\n")); got != 536870912 {
		t.Fatalf("got %d, want 536870912", got)
	}
}

func TestFromCgroup_V1_UnlimitedIsZero(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "sys/fs/cgroup/memory/memory.limit_in_bytes", "9223372036854771712\n")

	if got := FromCgroup(root, []byte("4:memory:/\n")); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
}

func TestFromCgroup_NoCgroupFsIsZero(t *testing.T) {
	if got := FromCgroup(t.TempDir(), nil); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
}
