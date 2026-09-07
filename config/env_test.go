package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type envCfg struct {
	Password string            `field:"password"`
	Host     string            `field:"host"`
	Plain    string            `field:"plain"`
	Hosts    []string          `field:"hosts"`
	Tags     map[string]string `field:"tags"`
	Nested   envSub            `field:"nested"`
	Port     int               `field:"port"`
}

type envSub struct {
	Token string `field:"token"`
}

// TestExpandString 覆盖展开规则本身。
func TestExpandString(t *testing.T) {
	t.Setenv("VOLTS_TEST_A", "aaa")
	t.Setenv("VOLTS_TEST_EMPTY", "")

	ok := []struct{ in, want string }{
		{"plain", "plain"},
		{"${VOLTS_TEST_A}", "aaa"},
		{"prefix-${VOLTS_TEST_A}-suffix", "prefix-aaa-suffix"},
		{"${VOLTS_TEST_A}${VOLTS_TEST_A}", "aaaaaa"},
		{"${VOLTS_TEST_EMPTY}", ""},
		{"${VOLTS_TEST_MISSING:-fallback}", "fallback"},
		{"${VOLTS_TEST_A:-fallback}", "aaa"},
		{"$${NOT_A_REF}", "${NOT_A_REF}"},
		{"postgres://u:${VOLTS_TEST_A}@h/db", "postgres://u:aaa@h/db"},
	}
	for _, c := range ok {
		got, err := expandString(c.in)
		if err != nil {
			t.Errorf("expandString(%q) 报错: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("expandString(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	if _, err := expandString("${VOLTS_TEST_MISSING}"); err == nil {
		t.Error("引用未设置的环境变量应报错")
	} else if !strings.Contains(err.Error(), "VOLTS_TEST_MISSING") {
		t.Errorf("错误信息应点名变量: %v", err)
	}
}

// TestExpandEnvRefs_InModel 锁定：各类字符串字段都参与展开，非字符串不受影响。
func TestExpandEnvRefs_InModel(t *testing.T) {
	t.Setenv("VOLTS_TEST_PW", "s3cret")
	t.Setenv("VOLTS_TEST_HOST", "db.internal:5432")
	t.Setenv("VOLTS_TEST_TOKEN", "tok")

	fn := "env_model.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{"envsec":{
		"password": "${VOLTS_TEST_PW}",
		"host": "${VOLTS_TEST_HOST}",
		"plain": "no-refs-here",
		"hosts": ["${VOLTS_TEST_HOST}", "literal"],
		"tags": {"k": "${VOLTS_TEST_TOKEN}"},
		"nested": {"token": "${VOLTS_TEST_TOKEN}"},
		"port": 5432
	}}`)

	cfg := newIsolated(t, fn)
	m := &envCfg{}
	cfg.Bind("envsec", m)

	if m.Password != "s3cret" {
		t.Errorf("password 未展开: %q", m.Password)
	}
	if m.Host != "db.internal:5432" {
		t.Errorf("host 未展开: %q", m.Host)
	}
	if m.Plain != "no-refs-here" {
		t.Errorf("无引用的值被改动: %q", m.Plain)
	}
	if len(m.Hosts) != 2 || m.Hosts[0] != "db.internal:5432" || m.Hosts[1] != "literal" {
		t.Errorf("切片元素展开不对: %v", m.Hosts)
	}
	if m.Tags["k"] != "tok" {
		t.Errorf("map 值展开不对: %v", m.Tags)
	}
	if m.Nested.Token != "tok" {
		t.Errorf("嵌套 struct 未展开: %q", m.Nested.Token)
	}
	if m.Port != 5432 {
		t.Errorf("非字符串字段被影响: %d", m.Port)
	}
}

// TestExpandEnvRefs_MissingVarIsAnError 锁定：变量缺失被记为错误，而不是静默留空。
func TestExpandEnvRefs_MissingVarIsAnError(t *testing.T) {
	fn := "env_missing.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{"envsec":{"password":"${VOLTS_TEST_DEFINITELY_MISSING}"}}`)

	cfg := newIsolated(t, fn)
	cfg.ClearErrors()
	cfg.Bind("envsec", &envCfg{})

	err := cfg.Errors()
	if err == nil {
		t.Fatal("引用未设置的环境变量没有被记为错误")
	}
	if !strings.Contains(err.Error(), "VOLTS_TEST_DEFINITELY_MISSING") {
		t.Errorf("错误信息应点名变量: %v", err)
	}
	if !strings.Contains(err.Error(), "envsec.password") {
		t.Errorf("错误信息应带上配置路径: %v", err)
	}
}

// TestExpandEnvRefs_NotWrittenBackToFile 锁定：展开后的值不回流到配置树，
// 因此任何一次保存都不会把密钥明文烤进配置文件。
func TestExpandEnvRefs_NotWrittenBackToFile(t *testing.T) {
	t.Setenv("VOLTS_TEST_PW2", "must-not-leak")

	fn := "env_noleak.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{"envsec":{"password":"${VOLTS_TEST_PW2}","plain":"x"}}`)

	cfg := newIsolated(t, fn)
	m := &envCfg{}
	cfg.Bind("envsec", m)
	if m.Password != "must-not-leak" {
		t.Fatalf("前置条件不成立，password 未展开: %q", m.Password)
	}

	cfg.SetValue("envsec.plain", "changed")
	if err := cfg.SaveToFile(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "must-not-leak") {
		t.Errorf("展开后的密钥被写进了配置文件:\n%s", raw)
	}
	var mm map[string]any
	if err := json.Unmarshal(raw, &mm); err != nil {
		t.Fatalf("解析落盘结果: %v\n%s", err, raw)
	}
	sec, _ := mm["envsec"].(map[string]any)
	if sec == nil || sec["password"] != "${VOLTS_TEST_PW2}" {
		t.Errorf("配置文件里应原样保留 ${VAR} 字面量, got %v", sec["password"])
	}
}

// TestReport_SkipsExternalizedSecrets 锁定：已写成 ${VAR} 的凭据不再被报为明文。
func TestReport_SkipsExternalizedSecrets(t *testing.T) {
	t.Setenv("VOLTS_TEST_PW3", "v")

	fn := "env_report.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{"envsec":{"password":"${VOLTS_TEST_PW3}","plain":"x"},
	                "other":{"db_password":"still-plaintext"}}`)

	cfg := newIsolated(t, fn)
	cfg.Bind("envsec", &envCfg{})

	hits := map[string]bool{}
	for _, s := range cfg.ReportAll().Secrets {
		hits[s.Path] = true
	}
	if hits["envsec.password"] {
		t.Error("已外置为 ${VAR} 的凭据仍被报成明文")
	}
	if !hits["other.db_password"] {
		t.Error("真正的明文凭据漏报了")
	}
}

// TestDump_MasksSecrets 锁定：Dump 生成的模板不含明文凭据。
func TestDump_MasksSecrets(t *testing.T) {
	fn := "env_dump.json"
	path := filepath.Join(AppPath, fn)
	tpl := "env_dump_tpl.json"
	tplPath := filepath.Join(AppPath, tpl)
	defer os.Remove(path)
	defer os.Remove(tplPath)

	write(t, path, `{"envsec":{"password":"literal-secret","plain":"visible"}}`)

	cfg := newIsolated(t, fn)
	cfg.Bind("envsec", &envCfg{})

	if err := cfg.Dump(tpl); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(tplPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "literal-secret") {
		t.Errorf("Dump 出的模板含明文凭据:\n%s", raw)
	}
	if !strings.Contains(string(raw), "${ENVSEC_PASSWORD}") {
		t.Errorf("凭据字段应被替换成 ${环境变量名} 占位符:\n%s", raw)
	}
	if !strings.Contains(string(raw), "visible") {
		t.Errorf("非凭据字段被误改:\n%s", raw)
	}
}

// TestGetString_ExpandsEnvRefs 锁定：GetString 与结构体装载路径对 ${VAR} 的处理一致。
func TestGetString_ExpandsEnvRefs(t *testing.T) {
	t.Setenv("VOLTS_TEST_GS", "resolved")

	fn := "env_getstring.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{"envsec":{"password":"${VOLTS_TEST_GS}","host":"${VOLTS_TEST_GS_MISSING}","plain":"raw"}}`)
	cfg := newIsolated(t, fn)

	if got := cfg.GetString("envsec.password", "fallback"); got != "resolved" {
		t.Errorf("GetString 未展开 ${VAR}: %q", got)
	}
	if got := cfg.GetString("envsec.plain", "fallback"); got != "raw" {
		t.Errorf("无引用的值被改动: %q", got)
	}
	if got := cfg.GetString("envsec.host", "fallback"); got != "fallback" {
		t.Errorf("变量缺失时应回落默认值, got %q", got)
	}
	if got := cfg.GetString("envsec.absent", "fallback"); got != "fallback" {
		t.Errorf("缺失键应回落默认值: %q", got)
	}
}
