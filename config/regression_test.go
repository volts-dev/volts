package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件是历史缺陷的回归测试。每一条都对应一个曾经真实存在、且被实测复现过的缺陷，
// 删掉任何一条都会让对应缺陷可以无声地回来。

type regParent struct {
	Value string `field:"value"`
}

type regChild struct {
	Port int `field:"port"`
}

// TestBind_ValuesLandInOwnTree 锁定：绑到命名树上的值落在**那棵树**里，不逃逸到默认树。
//
// 曾经的缺陷：Register 只把模型指针存进 core.models，模型内嵌的 Config.core 始终为 nil，
// 其 Save/Load 于是回落到全局 DEFAULT_PREFIX 的 core。表现为 New(name) 建出的命名配置
// 实体形同虚设 —— 注册两个模型后 SaveToFile() 写出的是 `{}`，值全进了全局单例。
func TestBind_ValuesLandInOwnTree(t *testing.T) {
	fn := "regression_owntree.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	cfg := newIsolated(t, fn)
	cfg.Bind("regowner.sub", &regChild{Port: 8080})

	if got := cfg.GetInt("regowner.sub.port", 0); got != 8080 {
		t.Errorf("本树里读不到值: got %d, want 8080", got)
	}
	if got := Default().GetInt("regowner.sub.port", 0); got != 0 {
		t.Errorf("值逃逸到了默认树: got %d", got)
	}

	if err := cfg.Dump(fn); err != nil {
		t.Fatalf("Dump: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
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

	write(t, path, `{"regowner":{"value":"from_file"}}`)

	cfg := newIsolated(t, fn)
	cfg.Bind("regowner.sub", &regChild{Port: 8080}) // 纯默认值，没人设定过
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

	owner, _ := m["regowner"].(map[string]any)
	if owner == nil || owner["value"] != "from_file" {
		t.Errorf("文件里原有的配置被丢弃: %s", raw)
	}
	rt, _ := m["runtime"].(map[string]any)
	if rt == nil || rt["explicit"] != "set_at_runtime" {
		t.Errorf("运行期显式设定的值没有落盘: %s", raw)
	}
	if owner != nil {
		if _, leaked := owner["sub"]; leaked {
			t.Errorf("代码默认值被写进了配置文件: %s", raw)
		}
	}
}

// TestLoadFromFile_DoesNotCreateFile 锁定：装载配置不会自动创建/回写配置文件。
//
// 除了会污染配置内容，自动回写还是多进程并发启动时把 config.json 写截断（服务器随后
// 静默退出而进程还活着）的直接原因。
func TestLoadFromFile_DoesNotCreateFile(t *testing.T) {
	fn := "regression_nocreate.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)
	os.Remove(path)

	cfg := newIsolated(t, fn)
	child := &regChild{Port: 8080}
	cfg.Bind("regowner.sub", child)

	if err := cfg.LoadFromFile(); err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		raw, _ := os.ReadFile(path)
		t.Errorf("LoadFromFile 自动创建了配置文件: %s", raw)
	}
	if child.Port != 8080 {
		t.Errorf("没有配置文件时结构体应保留默认值: got %d", child.Port)
	}
}

// TestGetDuration_UnitIsMillisecond 锁定：GetDuration 与写入端单位一致。
//
// 曾经的缺陷：写入端按 d.Milliseconds() 落盘、解码钩子按毫秒解回，而 GetDuration 是
// time.Duration(GetInt64(key)) —— 按纳秒解释，同一份数据两条读路径相差 100 万倍。
func TestGetDuration_UnitIsMillisecond(t *testing.T) {
	fn := "regression_duration.json"
	defer os.Remove(filepath.Join(AppPath, fn))
	cfg := newIsolated(t, fn)

	cfg.SetValue("x.timeout", int64(5000))
	if got := cfg.GetDuration("x.timeout", 0); got != 5*time.Second {
		t.Errorf("裸数字应按毫秒解释: got %v, want 5s", got)
	}
	cfg.SetValue("x.readable", "1500ms")
	if got := cfg.GetDuration("x.readable", 0); got != 1500*time.Millisecond {
		t.Errorf("带单位字符串: got %v, want 1.5s", got)
	}
	cfg.SetValue("x.garbage", "not-a-duration")
	if got := cfg.GetDuration("x.garbage", 7*time.Second); got != 7*time.Second {
		t.Errorf("不可解析时应回落默认值: got %v, want 7s", got)
	}
	if got := cfg.GetDuration("x.missing", 3*time.Second); got != 3*time.Second {
		t.Errorf("缺失键应回落默认值: got %v, want 3s", got)
	}
}

// TestUnbind_DoesNotWriteFile 锁定：Unbind 不顺手落盘。
//
// 曾经的缺陷：Unregister 内联调用 SaveToFile，装配期每改一次名就全量写一次盘；
// 多进程并发启动时会把同一个 config.json 写截断。
func TestUnbind_DoesNotWriteFile(t *testing.T) {
	fn := "regression_unbind.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	cfg := newIsolated(t, fn)
	cfg.Bind("regowner.sub", &regChild{Port: 8080})
	os.Remove(path)

	cfg.Unbind("regowner.sub")

	if _, err := os.Stat(path); err == nil {
		t.Error("Unbind 写了配置文件 —— 装配期并发写截断的回归")
	}
	if contains(cfg.Bound(), "regowner.sub") {
		t.Error("Unbind 之后节仍在树上")
	}
}

// TestBind_CollectsErrorsInsteadOfExiting 锁定：装载期错误累积可查，不 os.Exit。
//
// 曾经的缺陷：7 处 log.Fatal，一个坏 key 直接退进程；且格式化参数里根本没有 err，
// mapstructure 报出的字段名与原因被整个丢弃。
func TestBind_CollectsErrorsInsteadOfExiting(t *testing.T) {
	fn := "regression_errors.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)
	write(t, path, `{"regowner":{"sub":{"port":"not-a-number"}}}`)

	cfg := newIsolated(t, fn)
	cfg.ClearErrors()

	if err := cfg.Bind("regowner.sub", &regChild{Port: 8080}); err == nil {
		t.Error("坏配置应作为 Bind 的返回值报出")
	}
	err := cfg.Errors()
	if err == nil {
		t.Fatal("坏配置没有被记录为错误")
	}
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
// 曾经的缺陷：Register 把模型默认值经 SetValue 灌进 viper 的 override 层，而 override
// 优先级**高于**配置文件。于是启动时灌进去的默认值永久压住用户在文件里写的真实配置
// —— 改了配置没反应，且零提示。
func TestFileOverridesCodeDefaults(t *testing.T) {
	fn := "regression_precedence.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	cfg := newIsolated(t, fn)

	parent := &regParent{Value: "code_default"}
	cfg.Bind("regowner", parent)
	if parent.Value != "code_default" {
		t.Fatalf("文件不存在时应保留代码默认值: got %q", parent.Value)
	}

	write(t, path, `{"regowner":{"value":"user_wrote_this"}}`)
	if err := cfg.LoadFromFile(); err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	if got := cfg.GetString("regowner.value", "<none>"); got != "user_wrote_this" {
		t.Errorf("树里文件值被默认值压住: got %q", got)
	}
	if parent.Value != "user_wrote_this" {
		t.Errorf("结构体没拿到文件值: got %q", parent.Value)
	}

	// 再绑一个同路径的新对象：默认值仍只进 defaults 层，文件继续赢
	fresh := &regParent{Value: "another_default"}
	cfg.Bind("regowner", fresh)
	if fresh.Value != "user_wrote_this" {
		t.Errorf("再次 Bind 没拿到文件值: got %q", fresh.Value)
	}
	if got := cfg.GetString("regowner.value", "<none>"); got != "user_wrote_this" {
		t.Errorf("Bind 的默认值污染了高优先级层: got %q", got)
	}
}

// TestLoadFromFile_ContinuesAfterBadNode 锁定：一个节坏掉不影响其余节装载。
//
// 曾经的缺陷：Range 回调在保存失败时 `return false`，直接中止遍历 —— 遇错即停，
// 而不是把问题一次报全。
func TestLoadFromFile_ContinuesAfterBadNode(t *testing.T) {
	fn := "regression_continue.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	cfg := newIsolated(t, fn)
	child := &regChild{Port: 8080}
	parent := &regParent{Value: "default"}
	cfg.Bind("regowner.sub", child)
	cfg.Bind("regowner", parent)
	cfg.ClearErrors()

	write(t, path, `{"regowner":{"value":"ok","sub":{"port":"not-a-number"}}}`)
	cfg.LoadFromFile()

	if cfg.Errors() == nil {
		t.Error("坏节没有被记录为错误")
	}
	if parent.Value != "ok" {
		t.Errorf("坏节中断了后续节的装载: parent.Value = %q, want %q", parent.Value, "ok")
	}
	if child.Port != 8080 {
		t.Errorf("解码失败的节不该被写成半新半旧: %d", child.Port)
	}
}

/*
配置文件必须能从**工作目录**找到，而不只是可执行文件旁边。

`AppPath` 是可执行文件所在目录，对部署好的服务是对的；但 `go test` 把测试二进制编到
临时目录里跑，于是包目录下那份 config.json **一次都没被读过**。这不是报错，是每个
配置模型都停在代码默认值上 —— vectors 那边 storage 的测试因此拿到空 bucket_name，
报出来的却是 OSS SDK 的 "bucket name len is between [3-63], now is 0"。
*/
func TestLoadFromFile_FallsBackToWorkingDir(t *testing.T) {
	fn := "regression_cwd_fallback.json"

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwdPath := filepath.Join(cwd, fn)
	write(t, cwdPath, `{"regowner":{"value":"from_cwd"}}`)
	defer os.Remove(cwdPath)

	if _, err := os.Stat(filepath.Join(AppPath, fn)); err == nil {
		t.Skipf("AppPath 下已存在 %s，本用例无法区分两条路径", fn)
	}

	cfg := newIsolated(t, fn)
	model := &regParent{Value: "code_default"}
	cfg.Bind("regowner", model)

	if got := cfg.GetString("regowner.value", "<none>"); got != "from_cwd" {
		t.Errorf("树没读到工作目录里的配置: got %q, want %q", got, "from_cwd")
	}
	if model.Value != "from_cwd" {
		t.Errorf("结构体没拿到工作目录里的配置: got %q, want %q", model.Value, "from_cwd")
	}
}

// 可执行文件旁边有就用那份 —— 回落不能反过来抢走正常部署的配置。
func TestLoadFromFile_AppPathWinsOverWorkingDir(t *testing.T) {
	fn := "regression_apppath_wins.json"

	appPathFile := filepath.Join(AppPath, fn)
	write(t, appPathFile, `{"regowner":{"value":"from_apppath"}}`)
	defer os.Remove(appPathFile)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if cwd != AppPath {
		cwdFile := filepath.Join(cwd, fn)
		write(t, cwdFile, `{"regowner":{"value":"from_cwd"}}`)
		defer os.Remove(cwdFile)
	}

	cfg := newIsolated(t, fn)
	model := &regParent{Value: "code_default"}
	cfg.Bind("regowner", model)
	if model.Value != "from_apppath" {
		t.Errorf("回落抢走了 AppPath 的配置: got %q, want %q", model.Value, "from_apppath")
	}
}

// LoadFromFile 的 fileName 参数此前被整个忽略：签名收了它，函数体只用 core.FileName。
func TestLoadFromFile_HonoursExplicitFileName(t *testing.T) {
	fn := "regression_explicit_name.json"
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cwd, fn)
	write(t, path, `{"regowner":{"value":"explicit"}}`)
	defer os.Remove(path)

	cfg := newIsolated(t, "regression_not_this_one.json")
	model := &regParent{Value: "code_default"}
	cfg.Bind("regowner", model)

	if err := cfg.LoadFromFile(fn); err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	if model.Value != "explicit" {
		t.Errorf("显式文件名被忽略: got %q, want %q", model.Value, "explicit")
	}
}
