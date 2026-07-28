package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type envCfg struct {
	Config
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

func (self *envCfg) String() string { return "envsec" }

// TestExpandString 覆盖展开规则本身。
func TestExpandString(t *testing.T) {
	t.Setenv("VOLTS_TEST_A", "aaa")
	t.Setenv("VOLTS_TEST_EMPTY", "")

	ok := []struct{ in, want string }{
		{"plain", "plain"},
		{"${VOLTS_TEST_A}", "aaa"},
		{"prefix-${VOLTS_TEST_A}-suffix", "prefix-aaa-suffix"},
		{"${VOLTS_TEST_A}${VOLTS_TEST_A}", "aaaaaa"},
		{"${VOLTS_TEST_EMPTY}", ""},                                    // 已设置但为空 —— 用它，不报错
		{"${VOLTS_TEST_MISSING:-fallback}", "fallback"},                // 显式声明可缺省
		{"${VOLTS_TEST_A:-fallback}", "aaa"},                           // 有值时默认值不生效
		{"$${NOT_A_REF}", "${NOT_A_REF}"},                              // 转义成字面量
		{"postgres://u:${VOLTS_TEST_A}@h/db", "postgres://u:aaa@h/db"}, // 连接串内嵌
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

	// 未设置且未声明可缺省 —— 必须报错，不能静默留空
	if _, err := expandString("${VOLTS_TEST_MISSING}"); err == nil {
		t.Error("引用未设置的环境变量应报错")
	} else if !strings.Contains(err.Error(), "VOLTS_TEST_MISSING") {
		t.Errorf("错误信息应点名变量: %v", err)
	}
}

// TestExpandEnvRefs_InModel 锁定：模型各类字符串字段都参与展开，非字符串不受影响。
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
	cfg.Register(m)

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
//
// 密钥留空会让服务带着空密钥继续跑起来 —— 签名校验退化成人人可伪造，比起不来危险。
func TestExpandEnvRefs_MissingVarIsAnError(t *testing.T) {
	fn := "env_missing.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{"envsec":{"password":"${VOLTS_TEST_DEFINITELY_MISSING}"}}`)

	cfg := newIsolated(t, fn)
	cfg.ClearErrors()
	cfg.Register(&envCfg{})

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
//
// 外置密钥的全部意义就在这里 —— 若解析出的值回写配置树，第一次 SaveToFile 就前功尽弃，
// 而且是静默的：文件里悄悄从 ${VAR} 变成了真密钥。
func TestExpandEnvRefs_NotWrittenBackToFile(t *testing.T) {
	t.Setenv("VOLTS_TEST_PW2", "must-not-leak")

	fn := "env_noleak.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{"envsec":{"password":"${VOLTS_TEST_PW2}","plain":"x"}}`)

	cfg := newIsolated(t, fn)
	m := &envCfg{}
	cfg.Register(m)
	if m.Password != "must-not-leak" {
		t.Fatalf("前置条件不成立，password 未展开: %q", m.Password)
	}

	// 运行期改点别的，然后保存
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
//
// 否则报告会倒过来催人把已经外置好的密钥改回明文，而一份自相矛盾的安全提示
// 比没有更糟。
func TestReport_SkipsExternalizedSecrets(t *testing.T) {
	t.Setenv("VOLTS_TEST_PW3", "v")

	fn := "env_report.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{"envsec":{"password":"${VOLTS_TEST_PW3}","plain":"x"},
	                "other":{"db_password":"still-plaintext"}}`)

	cfg := newIsolated(t, fn)
	cfg.Register(&envCfg{})

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
