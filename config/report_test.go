package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type repCfg struct {
	Timeout int `field:"timeout"`
	Ttl     int `field:"ttl"`
}

// TestReport_FindsValueConflicts 锁定：同构节之间的取值分歧能被报出来。
//
// kylin 的真实配置里 ttl 在 16 个 registry 节中就有 0 和 60000 两个值。
func TestReport_FindsValueConflicts(t *testing.T) {
	fn := "report_conflict.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{
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
}`)

	cfg := newIsolated(t, fn)
	cfg.Bind("repsec", &repCfg{})

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
	for _, c := range rep.Conflicts {
		if c.Field == "timeout" {
			t.Errorf("取值一致的字段被误报为分歧: %+v", c)
		}
	}
}

// TestReport_SecretDetectionIgnoresNumbers 锁定：密钥识别不把时长/长度误报成凭据。
func TestReport_SecretDetectionIgnoresNumbers(t *testing.T) {
	fn := "report_secret.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{
  "repsec": {
    "timeout": 1,
    "ttl": 2,
    "auth_secret_key": "deadbeef",
    "db_password": "hunter2",
    "access_token_duration_time": 900,
    "password_length": 6,
    "empty_secret": ""
  }
}`)

	cfg := newIsolated(t, fn)
	cfg.Bind("repsec", &repCfg{})

	hits := map[string]bool{}
	for _, s := range cfg.ReportAll().Secrets {
		hits[s.Path] = true
	}
	for _, want := range []string{"repsec.auth_secret_key", "repsec.db_password"} {
		if !hits[want] {
			t.Errorf("真凭据漏报: %s", want)
		}
	}
	for _, bad := range []string{
		"repsec.access_token_duration_time",
		"repsec.password_length",
		"repsec.empty_secret",
	} {
		if hits[bad] {
			t.Errorf("误报为凭据: %s", bad)
		}
	}
}

// TestReport_FindsOrphanSections 锁定：无人认领的配置节能被报出来，而已绑定结构体的
// 子结构体节、map 字段下的键不算孤儿。
func TestReport_FindsOrphanSections(t *testing.T) {
	fn := "report_orphan.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{
  "repsec": {
    "timeout": 1,
    "ttl": 2
  },
  "svc": {
    "addr": ":1",
    "child": {
      "port": 2
    }
  },
  "m": {
    "tags": {"k": "v", "k2": "v2"}
  },
  "nobody_owns_this": {
    "a": 1,
    "b": 2
  }
}`)

	type withMap struct {
		Tags map[string]string
	}
	cfg := newIsolated(t, fn)
	cfg.Bind("repsec", &repCfg{})
	cfg.Bind("svc", &svcCfg{Child: &childCfg{}})
	cfg.Bind("m", &withMap{})

	rep := cfg.ReportAll()
	var found bool
	for _, o := range rep.Orphans {
		switch o.Path {
		case "nobody_owns_this":
			found = true
			if o.Keys != 2 {
				t.Errorf("孤儿节的项数不对: got %d, want 2", o.Keys)
			}
		case "repsec", "svc", "svc.child", "m.tags":
			t.Errorf("已绑定结构体覆盖的节被报成孤儿: %s", o.Path)
		}
	}
	if !found {
		t.Error("孤儿节没有被报出来")
	}
	if !strings.Contains(rep.String(), "nobody_owns_this") {
		t.Error("渲染出的报告里没有孤儿节")
	}
}
