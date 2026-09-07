package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type valOuter struct {
	Timeout int `field:"timeout"`
}

func (self *valOuter) Validate() error {
	if self.Timeout <= 0 {
		return Invalid("timeout", "必须为正，当前 %d", self.Timeout)
	}
	return nil
}

type valInner struct {
	Port int    `field:"port"`
	Host string `field:"host"`
}

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

type ordOuter struct {
	Deep *ordMid `field:"deep"`
}

func (self *ordOuter) Validate() error {
	valOrder = append(valOrder, "outer")
	return nil
}

type ordMid struct {
	Inner ordInner `field:"inner"`
}

type ordInner struct{ X int }

func (self *ordInner) Validate() error {
	valOrder = append(valOrder, "inner")
	return nil
}

// TestValidate_AddsPathAndLine 锁定：校验错误自动带上完整配置路径与文件行号。
//
// 业务代码只写相对字段名 —— 组件不知道自己被挂在配置树的哪个位置，路径只有配置树知道。
func TestValidate_AddsPathAndLine(t *testing.T) {
	fn := "validate_path.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{
  "valouter": {
    "timeout": -5
  }
}`)

	cfg := newIsolated(t, fn)
	cfg.Bind("valouter", &valOuter{Timeout: 30})

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

// TestValidate_ReportsAllFieldsAtOnce 锁定：一个结构体报多个字段时逐条带各自的定位。
func TestValidate_ReportsAllFieldsAtOnce(t *testing.T) {
	fn := "validate_multi.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{
  "valouter": {
    "timeout": 5,
    "inner": {
      "port": 0,
      "host": ""
    }
  }
}`)

	cfg := newIsolated(t, fn)
	cfg.Bind("valouter", &valOuter{Timeout: 30})
	cfg.Bind("valouter.inner", &valInner{})

	err := cfg.ValidateAll()
	if err == nil {
		t.Fatal("非法配置没有被校验出来")
	}
	msg := err.Error()
	t.Logf("校验输出:\n%s", msg)

	if !strings.Contains(msg, "valouter.inner.port") || !strings.Contains(msg, ":5") {
		t.Errorf("port 那条缺路径或行号（应为第 5 行）: %s", msg)
	}
	if !strings.Contains(msg, "valouter.inner.host") || !strings.Contains(msg, ":6") {
		t.Errorf("host 那条缺路径或行号（应为第 6 行）: %s", msg)
	}
	if strings.Contains(msg, "valouter.timeout") {
		t.Errorf("合法字段被误报: %s", msg)
	}
}

// TestValidate_NestedChildPath 锁定：拥有的子结构体自己的 Validate 报出来时，路径是它在
// 父结构体里的位置 —— 子组件不知道也不需要知道自己被挂在哪。
func TestValidate_NestedChildPath(t *testing.T) {
	fn := "validate_nested.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{
  "svc": {
    "timeout": 5,
    "inner": {
      "port": -1,
      "host": "h"
    }
  }
}`)

	type svc struct {
		Timeout int       `field:"timeout"`
		Inner   *valInner `field:"inner"`
	}
	cfg := newIsolated(t, fn)
	cfg.Bind("svc", &svc{Inner: &valInner{Port: 80, Host: "x"}})

	err := cfg.ValidateAll()
	if err == nil {
		t.Fatal("子结构体的非法配置没有被校验出来")
	}
	if !strings.Contains(err.Error(), "svc.inner.port") || !strings.Contains(err.Error(), ":5") {
		t.Errorf("子结构体错误缺路径或行号: %s", err)
	}
}

// TestValidate_BottomUp 锁定：自底向上校验，父的 Validate 可假设子已验过 ——
// 根之间按路径深度，根之内按结构体嵌套。
func TestValidate_BottomUp(t *testing.T) {
	fn := "validate_order.json"
	defer os.Remove(filepath.Join(AppPath, fn))

	cfg := newIsolated(t, fn)
	valOrder = nil
	cfg.Bind("ord", &ordOuter{Deep: &ordMid{}})
	if err := cfg.ValidateAll(); err != nil {
		t.Fatalf("不该有错误: %v", err)
	}
	if len(valOrder) != 2 || valOrder[0] != "inner" || valOrder[1] != "outer" {
		t.Errorf("结构体内校验顺序应为自底向上（inner 先于 outer）: got %v", valOrder)
	}

	valOrder = nil
	cfg2 := newIsolated(t, fn)
	cfg2.Bind("a", &valOuter{Timeout: 1})
	cfg2.Bind("a.b.c", &valInner{Port: 1, Host: "h"})
	cfg2.Bind("z", &ordInner{})
	cfg2.Bind("z.deep", &ordOuter{})
	if err := cfg2.ValidateAll(); err != nil {
		t.Fatalf("不该有错误: %v", err)
	}
	if len(valOrder) != 2 || valOrder[0] != "outer" || valOrder[1] != "inner" {
		t.Errorf("根之间应按深度自底向上（z.deep 先于 z）: got %v", valOrder)
	}
}

// TestValidate_SkipsWithoutValidator 锁定：不实现 IValidator 的结构体被跳过。
func TestValidate_SkipsWithoutValidator(t *testing.T) {
	fn := "validate_skip.json"
	defer os.Remove(filepath.Join(AppPath, fn))

	cfg := newIsolated(t, fn)
	cfg.Bind("regowner.sub", &regChild{Port: 80})
	if err := cfg.ValidateAll(); err != nil {
		t.Errorf("未实现 IValidator 的结构体不该产生错误: %v", err)
	}
}

type valMixedCase struct {
	Endpoint string `field:"endpoint"`
}

func (self *valMixedCase) Validate() error {
	if self.Endpoint == "" {
		return Invalid("endpoint", "不能为空")
	}
	return nil
}

// TestValidate_PathIsLowercased 锁定：错误里的配置路径一律小写。
//
// 配置键在 viper 里本就大小写不敏感，文件里写的是 mixedcase；若照 Bind 时给的
// 原样打印 MixedCase.endpoint，用户拿这个路径去配置文件里搜是搜不到的。
func TestValidate_PathIsLowercased(t *testing.T) {
	fn := "validate_case.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)
	write(t, path, `{"mixedcase":{"endpoint":""}}`)

	cfg := newIsolated(t, fn)
	cfg.Bind("MixedCase", &valMixedCase{})

	err := cfg.ValidateAll()
	if err == nil {
		t.Fatal("非法配置没有被校验出来")
	}
	if strings.Contains(err.Error(), "MixedCase") {
		t.Errorf("路径未小写化，用户在配置文件里搜不到: %s", err)
	}
	if !strings.Contains(err.Error(), "mixedcase.endpoint") {
		t.Errorf("缺少小写的完整路径: %s", err)
	}
}
