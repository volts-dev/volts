package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件是 P0 四项修复的回归测试。每一条都对应一个曾经真实存在、且被实测复现过的缺陷，
// 删掉任何一条都会让对应缺陷可以无声地回来。

type regParent struct {
	Config
	Value string `field:"value"`
}

func (self *regParent) String() string { return "regowner" }

type regChild struct {
	Config
	Port int `field:"port"`
}

func (self *regChild) String() string { return "regowner.sub" }

// TestRegister_BindsCoreToModel 锁定：Register 必须把自己的 core 注入模型内嵌的 Config。
//
// 曾经的缺陷：Register 只把模型指针存进 core.models，模型内嵌的 Config.core 始终为 nil，
// 其 Save/Load 于是回落到全局 DEFAULT_PREFIX 的 core。表现为 New(name) 建出的命名配置
// 实体形同虚设 —— 注册两个模型后 SaveToFile() 写出的是 `{}`，值全进了全局单例。
func TestRegister_BindsCoreToModel(t *testing.T) {
	fn := "regression_bindcore.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	cfg := New("regression_bindcore", WithFileName(fn))

	child := &regChild{Port: 8080}
	cfg.Register(child)

	if child.Config.Core() != cfg.Core() {
		t.Fatal("模型内嵌的 Config 没有绑定到注册方的 core —— core 逃逸回归")
	}

	// 值必须落在注册方的 core 里，而不是全局单例
	if got := cfg.GetInt("regowner.sub.port", 0); got != 8080 {
		t.Errorf("注册方 core 里读不到值: got %d, want 8080", got)
	}

	// Dump 输出全量（含默认值），可用来证明值确实进了**本实例**的 core 而非全局单例。
	// 这里不能用 SaveToFile 做判据 —— 它只写差异，纯默认值本就不该出现在里面。
	if err := cfg.Dump(fn); err != nil {
		t.Fatalf("Dump: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回配置文件: %v", err)
	}
	if strings.TrimSpace(string(raw)) == "{}" {
		t.Fatal("Dump 结果为空对象 —— 注册的模型没有写进本实例的 core")
	}

	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("解析 Dump 结果: %v", err)
	}
	owner, ok := m["regowner"].(map[string]any)
	if !ok {
		t.Fatalf("Dump 结果缺少 regowner 节: %s", raw)
	}
	if _, ok := owner["sub"]; !ok {
		t.Errorf("Dump 结果缺少 regowner.sub 节: %s", raw)
	}
}

// TestSaveToFile_OnlyPersistsDiff 锁定：落盘只写差异，代码默认值不落盘。
//
// 曾经的缺陷：LoadFromFile 末尾 `if changed && AutoCreateFile { SaveToFile() }`，
// 只要有模型的配置节不在文件里就把默认值刷回磁盘。后果是配置文件膨胀成全量快照
// （kylin 实测 453 个 key，400+ 是默认值，光 registry.timeout 就复制了 15 份），
// 而默认值一旦落盘就成了「用户配置」—— 此后改代码里的默认值再也不生效。
func TestSaveToFile_OnlyPersistsDiff(t *testing.T) {
	fn := "regression_diff.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	// 文件里预先只有一项，代表「用户真的写过的配置」
	if err := os.WriteFile(path, []byte(`{"regowner":{"value":"from_file"}}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := New("regression_diff", WithFileName(fn))
	child := &regChild{Port: 8080} // 纯默认值，没人设定过
	cfg.Register(child)

	// 运行期显式设定一项
	cfg.SetValue("runtime.explicit", "set_at_runtime")

	if err := cfg.SaveToFile(); err != nil {
		t.Fatalf("SaveToFile: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("解析落盘结果: %v\n%s", err, raw)
	}

	// 文件里本来就有的，必须保留 —— 否则一次保存就把用户配置抹了
	owner, _ := m["regowner"].(map[string]any)
	if owner == nil || owner["value"] != "from_file" {
		t.Errorf("文件里原有的配置被丢弃: %s", raw)
	}
	// 运行期显式设定的，必须落盘
	rt, _ := m["runtime"].(map[string]any)
	if rt == nil || rt["explicit"] != "set_at_runtime" {
		t.Errorf("运行期显式设定的值没有落盘: %s", raw)
	}
	// 纯默认值，不得落盘
	if owner != nil {
		if _, leaked := owner["sub"]; leaked {
			t.Errorf("代码默认值被写进了配置文件: %s", raw)
		}
	}
}

// TestLoadFromFile_DoesNotCreateFile 锁定：装载配置不会自动创建/回写配置文件。
//
// 取消的是 LoadFromFile 末尾的自动回写。除了会污染配置内容，它还是多进程并发
// 启动时把 config.json 写截断（服务器随后静默退出而进程还活着）的直接原因。
func TestLoadFromFile_DoesNotCreateFile(t *testing.T) {
	fn := "regression_nocreate.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)
	os.Remove(path)

	cfg := New("regression_nocreate", WithFileName(fn))
	child := &regChild{Port: 8080}
	cfg.Register(child)

	if err := cfg.LoadFromFile(); err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}

	if _, err := os.Stat(path); err == nil {
		raw, _ := os.ReadFile(path)
		t.Errorf("LoadFromFile 自动创建了配置文件: %s", raw)
	}

	// 但模型仍应拿到默认值，正常跑
	if child.Port != 8080 {
		t.Errorf("没有配置文件时模型应保留默认值: got %d", child.Port)
	}
}

// TestGetDuration_UnitIsMillisecond 锁定：GetDuration 与写入端单位一致。
//
// 曾经的缺陷：SaveFromModel 按 d.Milliseconds() 落盘、withDurationHook 按毫秒解回，
// 而 GetDuration 是 time.Duration(GetInt64(key)) —— 按纳秒解释。同一份数据两条读路径
// 相差 100 万倍（5000 读成 5µs 而非 5s）。
func TestGetDuration_UnitIsMillisecond(t *testing.T) {
	fn := "regression_duration.json"
	defer os.Remove(filepath.Join(AppPath, fn))
	cfg := New("regression_duration", WithFileName(fn))

	cfg.SetValue("x.timeout", int64(5000))
	if got := cfg.GetDuration("x.timeout", 0); got != 5*time.Second {
		t.Errorf("裸数字应按毫秒解释: got %v, want 5s", got)
	}

	// 带单位字符串是人手写配置的首选形态
	cfg.SetValue("x.readable", "1500ms")
	if got := cfg.GetDuration("x.readable", 0); got != 1500*time.Millisecond {
		t.Errorf("带单位字符串: got %v, want 1.5s", got)
	}

	// 无法解析时回落默认值，而不是 0
	cfg.SetValue("x.garbage", "not-a-duration")
	if got := cfg.GetDuration("x.garbage", 7*time.Second); got != 7*time.Second {
		t.Errorf("不可解析时应回落默认值: got %v, want 7s", got)
	}

	if got := cfg.GetDuration("x.missing", 3*time.Second); got != 3*time.Second {
		t.Errorf("缺失键应回落默认值: got %v, want 3s", got)
	}
}

// TestUnregister_DoesNotWriteFile 锁定：Unregister 不再顺手落盘。
//
// 曾经的缺陷：Unregister 内联调用 SaveToFile。它的主要调用方是 WithConfigPrefixName /
// Name 这类改名 option，装配期每改一次名就全量写一次盘；多进程并发启动时会把同一个
// config.json 写截断，服务器随后静默退出而进程还活着。
func TestUnregister_DoesNotWriteFile(t *testing.T) {
	fn := "regression_unregister.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	cfg := New("regression_unregister", WithFileName(fn))
	child := &regChild{Port: 8080}
	cfg.Register(child)

	if _, err := os.Stat(path); err == nil {
		os.Remove(path)
	}

	cfg.Unregister(child)

	if _, err := os.Stat(path); err == nil {
		t.Error("Unregister 写了配置文件 —— 装配期并发写截断的回归")
	}
}

// TestRegister_CollectsErrorsInsteadOfExiting 锁定：装载期错误累积可查，不再 os.Exit。
//
// 曾经的缺陷：Register / LoadFromFile / Reload 里 7 处 log.Fatal，一个坏 key 直接退进程；
// 且 Fatalf 的格式化参数里根本没有 err，mapstructure 报出的字段名与原因被整个丢弃，
// 用户只看得到 "load xxx config failed!"。
func TestRegister_CollectsErrorsInsteadOfExiting(t *testing.T) {
	fn := "regression_errors.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	// 文件里把 port 写成不可解析为 int 的值
	if err := os.WriteFile(path, []byte(`{"regowner":{"sub":{"port":"not-a-number"}}}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := New("regression_errors", WithFileName(fn))
	cfg.ClearErrors()

	child := &regChild{Port: 8080}
	cfg.Register(child) // 曾经：此处 os.Exit(1)，测试根本跑不到下一行

	err := cfg.Errors()
	if err == nil {
		t.Fatal("坏配置没有被记录为错误")
	}
	// 错误信息必须同时带上配置路径和底层原因，否则定位不了是哪一份配置
	msg := err.Error()
	if !strings.Contains(msg, "regowner.sub") {
		t.Errorf("错误信息缺少配置路径: %v", msg)
	}
	if !strings.Contains(msg, "port") {
		t.Errorf("错误信息丢失了底层原因（字段名）: %v", msg)
	}

	cfg.ClearErrors()
	if cfg.Errors() != nil {
		t.Error("ClearErrors 之后仍有残留错误")
	}
}

// TestFileOverridesCodeDefaults 锁定：配置文件的值必须盖得过代码默认值。
//
// 曾经的缺陷（P0-a 修好 core 逃逸后才暴露）：Register 把模型默认值经 SetValue 灌进
// viper 的 override 层，而 override 优先级**高于**配置文件。于是启动时灌进去的默认值
// 永久压住用户在文件里写的真实配置 —— 改了配置没反应，且零提示。
//
// 它平时被「首次启动自动把默认值回写进文件、第二次启动 InConfig 为真就不再灌」的行为
// 掩盖着。真正致命的是两条灌值路径：Register 一条、LoadToModel 里「文件没有该节就
// model.Save() 兜底」又一条 —— 只修前者、漏掉后者，现象一模一样。本测试同时覆盖两条。
func TestFileOverridesCodeDefaults(t *testing.T) {
	fn := "regression_precedence.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	cfg := New("regression_precedence", WithFileName(fn))

	// 路径一：Register 时文件不存在，默认值必须进 defaults 层
	parent := &regParent{Value: "code_default"}
	cfg.Register(parent)
	if parent.Value != "code_default" {
		t.Fatalf("首次注册应保留代码默认值: got %q", parent.Value)
	}

	// 用户手写配置文件
	if err := os.WriteFile(path, []byte(`{"regowner":{"value":"user_wrote_this"}}`), 0644); err != nil {
		t.Fatal(err)
	}

	if err := cfg.LoadFromFile(); err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	if got := cfg.GetString("regowner.value", "<none>"); got != "user_wrote_this" {
		t.Errorf("core 里文件值被默认值压住: got %q, want %q", got, "user_wrote_this")
	}
	if parent.Value != "user_wrote_this" {
		t.Errorf("模型没拿到文件值: got %q, want %q", parent.Value, "user_wrote_this")
	}

	// 路径二：LoadToModel 对一个未注册的模型做兜底 Save，同样不得污染 override 层
	fresh := &regParent{Value: "another_default"}
	if err := cfg.LoadToModel(fresh); err != nil {
		t.Fatalf("LoadToModel: %v", err)
	}
	if fresh.Value != "user_wrote_this" {
		t.Errorf("LoadToModel 没拿到文件值: got %q", fresh.Value)
	}
	if got := cfg.GetString("regowner.value", "<none>"); got != "user_wrote_this" {
		t.Errorf("LoadToModel 的兜底 Save 污染了高优先级层: got %q", got)
	}
}

// TestRegister_ContinuesAfterBadModel 锁定：一个模型坏掉不影响其余模型装载。
//
// 曾经的缺陷：LoadFromFile 的 Range 回调在保存失败时 `return false`，直接中止遍历，
// 后面所有模型都不再处理 —— 遇错即停，而不是把问题一次报全。
func TestRegister_ContinuesAfterBadModel(t *testing.T) {
	fn := "regression_continue.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	if err := os.WriteFile(path, []byte(
		`{"regowner":{"value":"ok","sub":{"port":"not-a-number"}}}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := New("regression_continue", WithFileName(fn))
	cfg.ClearErrors()

	child := &regChild{Port: 8080}
	parent := &regParent{Value: "default"}
	cfg.Register(child)  // 坏的
	cfg.Register(parent) // 好的，必须仍然被正确装载

	if cfg.Errors() == nil {
		t.Error("坏模型没有被记录为错误")
	}
	if parent.Value != "ok" {
		t.Errorf("坏模型中断了后续模型的装载: parent.Value = %q, want %q", parent.Value, "ok")
	}
}
