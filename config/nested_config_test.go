package config

import (
	"os"
	"path/filepath"
	"testing"
)

type (
	ParentConfig struct {
		Config
		Value string `field:"value"`
	}

	ChildConfig struct {
		Config
		Value string `field:"value"`
		Port  int    `field:"port"`
	}
)

func (self *ParentConfig) String() string { return "parent" }
func (self *ChildConfig) String() string  { return "parent.child" }

// TestNestedConfig 验证了框架加载具有点分路径嵌套关系的配置模型的能力。
func TestNestedConfig(t *testing.T) {
	testFileName := "nested_config_test.json"
	testFile := filepath.Join(AppPath, testFileName)
	//defer os.Remove(testFile)

	// 1. 创建独立的配置实例以避免单例状态冲突
	cfg := New("nested_test", WithFileName(testFileName))

	// 2. 注册具有父子层级关系的配置模型
	parent := &ParentConfig{Value: "p_init"}
	cfg.Register(parent)

	child := &ChildConfig{Value: "c_init", Port: 80}
	cfg.Register(child)

	// 3. 将初始默认值写入文件
	cfg.SaveToFile()

	// 4. 模拟外部（或手动）修改 JSON 文件
	newJson := `{
		"parent": {
			"value": "p_mod",
			"child": {
				"value": "c_mod",
				"port": 443
			}
		}
	}`
	os.WriteFile(testFile, []byte(newJson), 0644)

	// 5. 重新载入文件内容到内存
	if err := cfg.LoadFromFile(); err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	// 6. 将各模型与内存配置同步 (手动调用以确保指向正确的 cfg 实例)
	cfg.LoadToModel(parent)
	cfg.LoadToModel(child)

	// 7. 验证字段是否按照 JSON 层级正确加载
	t.Logf("Reloaded -> Parent value: %s, Child value: %s, port: %d", parent.Value, child.Value, child.Port)

	if parent.Value != "p_mod" {
		t.Errorf("expected parent_modified, got %s", parent.Value)
	}
	if child.Value != "c_mod" {
		t.Errorf("expected child_modified, got %s", child.Value)
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
		Config
		Title string    `field:"title"`
		Child SubStruct `field:"child"`
	}
)

func (self *NestedModel) String() string { return "nested" }

// TestSingleModelNesting 验证了单个模型内部通过结构体字段实现的嵌套加载。
func TestSingleModelNesting(t *testing.T) {
	testFileName := "single_model_nest_test.json"
	testFile := filepath.Join(AppPath, testFileName)
	defer os.Remove(testFile)

	cfg := New("single_nest_test", WithFileName(testFileName))

	nm := &NestedModel{}
	nm.checkSelf(nm)

	// 设置初始 JSON
	newJson := `{"nested":{"title":"level1","child":{"name":"level2"}}}`
	os.WriteFile(testFile, []byte(newJson), 0644)

	// 加载并解析到模型
	cfg.LoadFromFile()
	cfg.LoadToModel(nm)

	t.Logf("Single Model -> Title: %s, Child.Name: %s", nm.Title, nm.Child.Name)

	if nm.Title != "level1" || nm.Child.Name != "level2" {
		t.Errorf("Single model nesting failed: got %+v", nm)
	}
}
