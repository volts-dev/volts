package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type repCfg struct {
	Config
	Timeout int `field:"timeout"`
	Ttl     int `field:"ttl"`
}

func (self *repCfg) String() string { return "repsec" }

// TestReport_FindsValueConflicts 锁定：同构节之间的取值分歧能被报出来。
//
// 这是报告里最有价值的一类：结构相同的节，同一字段取值不同。若它们其实指向同一个
// 共享实例（registry.Use / broker.Use 按 name-addr 复用），哪一份生效取决于谁先
// 创建该实例 —— 这类「改了配置不生效」改对了地方也可能不生效。
// kylin 的真实配置里 ttl 在 16 个 registry 节中就有 0 和 60000 两个值。
func TestReport_FindsValueConflicts(t *testing.T) {
	fn := "report_conflict.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	content := `{
  "repsec": {
    "timeout": 5000,
    "ttl": 60000
  },
  "alpha": {
    "reg": {
      "timeout": 5000,
      "ttl": 60000
    }
  },
  "beta": {
    "reg": {
      "timeout": 5000,
      "ttl": 0
    }
  }
}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := newIsolated(t, fn)
	cfg.Register(&repCfg{})

	rep := cfg.ReportAll()
	t.Logf("\n%s", rep.String())

	var found *ValueConflict
	for i := range rep.Conflicts {
		if rep.Conflicts[i].Field == "ttl" {
			found = &rep.Conflicts[i]
		}
	}
	if found == nil {
		t.Fatal("ttl 的取值分歧没有被报出来")
	}
	if len(found.Sections) < 3 {
		t.Errorf("分歧涉及的节数不对: got %d, want >=3", len(found.Sections))
	}
	// timeout 三处都一样，不该报
	for _, c := range rep.Conflicts {
		if c.Field == "timeout" {
			t.Errorf("取值一致的字段被误报为分歧: %+v", c)
		}
	}
}

// TestReport_SecretDetectionIgnoresNumbers 锁定：密钥识别不把时长/长度误报成凭据。
//
// 光看字段名会大量误报：access_token_duration_time、password_length、
// refresh_token_duration_time 都命中了 token / password 却都是数字。实测对 kylin
// 的配置误报率一度是 45%（11 条里 5 条）—— 一份误报过半的安全提示，人扫两眼就会
// 连真的一起略过。
func TestReport_SecretDetectionIgnoresNumbers(t *testing.T) {
	fn := "report_secret.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	content := `{
  "repsec": {
    "timeout": 1,
    "ttl": 2,
    "auth_secret_key": "deadbeef",
    "db_password": "hunter2",
    "access_token_duration_time": 900,
    "password_length": 6,
    "empty_secret": ""
  }
}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := newIsolated(t, fn)
	cfg.Register(&repCfg{})

	hits := map[string]bool{}
	for _, s := range cfg.ReportAll().Secrets {
		hits[s.Path] = true
	}
	t.Logf("识别为凭据的: %v", hits)

	for _, want := range []string{"repsec.auth_secret_key", "repsec.db_password"} {
		if !hits[want] {
			t.Errorf("真凭据漏报: %s", want)
		}
	}
	for _, bad := range []string{
		"repsec.access_token_duration_time", // 命中 token，但是数字
		"repsec.password_length",            // 命中 password，但是数字
		"repsec.empty_secret",               // 命中 secret，但是空值
	} {
		if hits[bad] {
			t.Errorf("误报为凭据: %s", bad)
		}
	}
}

// TestReport_FindsOrphanSections 锁定：无人认领的配置节能被报出来。
//
// Lint 刻意跳过整节孤儿（配置模型惰性注册，报了就是假阳性）；Report 专门补上这一块，
// 并在输出里标注「若模块本次未加载属正常」。
func TestReport_FindsOrphanSections(t *testing.T) {
	fn := "report_orphan.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	content := `{
  "repsec": {
    "timeout": 1,
    "ttl": 2
  },
  "nobody_owns_this": {
    "a": 1,
    "b": 2
  }
}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := newIsolated(t, fn)
	cfg.Register(&repCfg{})

	rep := cfg.ReportAll()
	var found bool
	for _, o := range rep.Orphans {
		if o.Path == "nobody_owns_this" {
			found = true
			if o.Keys != 2 {
				t.Errorf("孤儿节的项数不对: got %d, want 2", o.Keys)
			}
		}
		if o.Path == "repsec" {
			t.Error("已注册的节被报成孤儿")
		}
	}
	if !found {
		t.Error("孤儿节没有被报出来")
	}

	if !strings.Contains(rep.String(), "nobody_owns_this") {
		t.Error("渲染出的报告里没有孤儿节")
	}
}
