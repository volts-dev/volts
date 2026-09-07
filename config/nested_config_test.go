package config

import (
	"os"
	"path/filepath"
	"testing"
)

type (
	ParentConfig struct {
		Value string `field:"value"`
	}

	ChildConfig struct {
		Value string `field:"value"`
		Port  int    `field:"port"`
	}
)

// TestNestedConfig 验证两个**独立的根节**可以有点分嵌套的路径（parent 与 parent.child），
// 各自按自己的路径装载 —— 这是「同一节由不同进程/组件各认领一段」的形状。
func TestNestedConfig(t *testing.T) {
	fn := "nested_config_test.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	cfg := newIsolated(t, fn)
	parent := &ParentConfig{Value: "p_init"}
	cfg.Bind("parent", parent)
	child := &ChildConfig{Value: "c_init", Port: 80}
	cfg.Bind("parent.child", child)

	write(t, path, `{
		"parent": {
			"value": "p_mod",
			"child": {
				"value": "c_mod",
				"port": 443
			}
		}
	}`)
	if err := cfg.LoadFromFile(); err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	if parent.Value != "p_mod" {
		t.Errorf("expected p_mod, got %s", parent.Value)
	}
	if child.Value != "c_mod" {
		t.Errorf("expected c_mod, got %s", child.Value)
	}
	if child.Port != 443 {
		t.Errorf("expected 443, got %d", child.Port)
	}
}

type (
	SubStruct struct {
		Name string `field:"name"`
	}
	NestedModel struct {
		Title string    `field:"title"`
		Child SubStruct `field:"child"`
	}
)

// TestSingleModelNesting 验证单个结构体内部通过**值类型**子结构体实现的嵌套装载。
func TestSingleModelNesting(t *testing.T) {
	fn := "single_model_nest_test.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)
	write(t, path, `{"nested":{"title":"level1","child":{"name":"level2"}}}`)

	cfg := newIsolated(t, fn)
	nm := &NestedModel{}
	cfg.Bind("nested", nm)

	if nm.Title != "level1" || nm.Child.Name != "level2" {
		t.Errorf("Single model nesting failed: got %+v", nm)
	}
	if !contains(cfg.knownSections(), "nested.child") {
		t.Errorf("值类型子结构体也应是子节: %v", cfg.knownSections())
	}
}
