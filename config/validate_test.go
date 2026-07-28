package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type valOuter struct {
	Config
	Timeout int `field:"timeout"`
}

func (self *valOuter) String() string { return "valouter" }
func (self *valOuter) Validate() error {
	if self.Timeout <= 0 {
		return Invalid("timeout", "必须为正，当前 %d", self.Timeout)
	}
	return nil
}

type valInner struct {
	Config
	Port int `field:"port"`
	Host string `field:"host"`
}

func (self *valInner) String() string { return "valouter.inner" }
func (self *valInner) Validate() error {
	var errs []error
	if self.Port <= 0 {
		errs = append(errs, Invalid("port", "端口必须为正"))
	}
	if self.Host == "" {
		errs = append(errs, Invalid("host", "不能为空"))
	}
	return errors.Join(errs...)
}

// 记录校验顺序，用于验证「自底向上」
var valOrder []string

type ordOuter struct{ Config }

func (self *ordOuter) String() string { return "ord" }
func (self *ordOuter) Validate() error {
	valOrder = append(valOrder, "outer")
	return nil
}

type ordInner struct{ Config }

func (self *ordInner) String() string { return "ord.deep.inner" }
func (self *ordInner) Validate() error {
	valOrder = append(valOrder, "inner")
	return nil
}

// TestValidate_AddsPathAndLine 锁定：校验错误自动带上完整配置路径与文件行号。
//
// 业务代码只写相对字段名 —— 组件不知道自己被挂在配置树的哪个位置（transport
// 可能在 server.http.transport 也可能在 client.rpc.transport），路径只有配置树
// 知道。让业务代码自己拼，拼错是迟早的事。
func TestValidate_AddsPathAndLine(t *testing.T) {
	fn := "validate_path.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	content := `{
  "valouter": {
    "timeout": -5
  }
}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := newIsolated(t, fn)
	cfg.Register(&valOuter{Timeout: 30})

	err := cfg.ValidateAll()
	if err == nil {
		t.Fatal("非法配置没有被校验出来")
	}
	msg := err.Error()
	t.Logf("校验输出: %s", msg)

	if !strings.Contains(msg, "valouter.timeout") {
		t.Errorf("错误信息缺少完整配置路径: %s", msg)
	}
	if !strings.Contains(msg, ":3") {
		t.Errorf("错误信息缺少文件行号（应为第 3 行）: %s", msg)
	}
	if !strings.Contains(msg, "必须为正") {
		t.Errorf("错误信息丢失了业务描述: %s", msg)
	}
}

// TestValidate_ReportsAllFieldsAtOnce 锁定：一个模型报多个字段时逐条带各自的定位。
//
// 合并成一条消息就又变回了「定位不了」——450 个 key 的文件里，「有两个字段不对」
// 这种消息等于没说。
func TestValidate_ReportsAllFieldsAtOnce(t *testing.T) {
	fn := "validate_multi.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	content := `{
  "valouter": {
    "timeout": 5,
    "inner": {
      "port": 0,
      "host": ""
    }
  }
}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := newIsolated(t, fn)
	cfg.Register(&valOuter{Timeout: 30})
	cfg.Register(&valInner{})

	err := cfg.ValidateAll()
	if err == nil {
		t.Fatal("非法配置没有被校验出来")
	}
	msg := err.Error()
	t.Logf("校验输出:\n%s", msg)

	// 两个字段各自成条，各带各的行号
	if !strings.Contains(msg, "valouter.inner.port") || !strings.Contains(msg, ":5") {
		t.Errorf("port 那条缺路径或行号（应为第 5 行）: %s", msg)
	}
	if !strings.Contains(msg, "valouter.inner.host") || !strings.Contains(msg, ":6") {
		t.Errorf("host 那条缺路径或行号（应为第 6 行）: %s", msg)
	}
	// 合法的字段不得误报
	if strings.Contains(msg, "valouter.timeout") {
		t.Errorf("合法字段被误报: %s", msg)
	}
}

// TestValidate_BottomUp 锁定：自底向上校验，父的 Validate 可假设子已验过。
func TestValidate_BottomUp(t *testing.T) {
	fn := "validate_order.json"
	defer os.Remove(filepath.Join(AppPath, fn))

	cfg := newIsolated(t, fn)
	valOrder = nil
	cfg.Register(&ordOuter{})
	cfg.Register(&ordInner{})

	if err := cfg.ValidateAll(); err != nil {
		t.Fatalf("不该有错误: %v", err)
	}
	if len(valOrder) != 2 || valOrder[0] != "inner" || valOrder[1] != "outer" {
		t.Errorf("校验顺序应为自底向上（inner 先于 outer）: got %v", valOrder)
	}
}

// TestValidate_SkipsModelsWithoutValidator 锁定：不实现 IValidator 的模型被跳过。
func TestValidate_SkipsModelsWithoutValidator(t *testing.T) {
	fn := "validate_skip.json"
	defer os.Remove(filepath.Join(AppPath, fn))

	cfg := newIsolated(t, fn)
	cfg.Register(&regChild{Port: 80}) // 没有 Validate 方法

	if err := cfg.ValidateAll(); err != nil {
		t.Errorf("未实现 IValidator 的模型不该产生错误: %v", err)
	}
}
