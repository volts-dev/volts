package config

import (
	"path/filepath"
	"testing"
)

type leavesChild struct {
	DataDir string
}

type leavesCfg struct {
	Root  string
	Dirs  []string
	Child *leavesChild
}

// TestLeaves_ReportsDefaultsAndFileValues 锁定：Leaves 读的是结构体里此刻的值——文件里写了的
// 取文件的，没写的取代码默认值；子结构体展开成子路径；切片返回副本。
func TestLeaves_ReportsDefaultsAndFileValues(t *testing.T) {
	fn := filepath.Join(t.TempDir(), "config.json")
	write(t, fn, `{"leavessec": {"root": "/from/file"}}`)
	cfg := newIsolated(t, fn)
	if err := cfg.LoadFromFile(); err != nil {
		t.Fatal(err)
	}
	c := &leavesCfg{Root: "/default", Dirs: []string{"/a"}, Child: &leavesChild{DataDir: "/default/data"}}
	if err := cfg.Bind("leavessec", c); err != nil {
		t.Fatal(err)
	}

	got := map[string]any{}
	for _, l := range cfg.Leaves() {
		got[l.Path] = l.Value
	}
	if got["leavessec.root"] != "/from/file" {
		t.Errorf("root = %v, want 文件里的 /from/file", got["leavessec.root"])
	}
	if got["leavessec.child.data_dir"] != "/default/data" {
		t.Errorf("child.data_dir = %v, want 默认值 /default/data", got["leavessec.child.data_dir"])
	}
	dirs, _ := got["leavessec.dirs"].([]string)
	if len(dirs) != 1 || dirs[0] != "/a" {
		t.Fatalf("dirs = %v", got["leavessec.dirs"])
	}
	dirs[0] = "/mutated"
	if c.Dirs[0] != "/a" {
		t.Error("Leaves 返回的切片与结构体共用底层数组")
	}
}

// TestLeaves_DedupsAdoptedChild 锁定：子结构体单独绑成根节时不重复出现。
func TestLeaves_DedupsAdoptedChild(t *testing.T) {
	cfg := newIsolated(t, filepath.Join(t.TempDir(), "config.json"))
	c := &leavesCfg{Child: &leavesChild{DataDir: "/x"}}
	cfg.Bind("leavessec.child", c.Child)
	cfg.Bind("leavessec", c)

	n := 0
	for _, l := range cfg.Leaves() {
		if l.Path == "leavessec.child.data_dir" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("leavessec.child.data_dir 出现 %d 次, want 1", n)
	}
}
