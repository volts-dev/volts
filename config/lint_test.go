package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type lintCfg struct {
	ReadTimeout time.Duration `field:"read_timeout"`
	MaxRegistry int           `field:"max_registry"`
	Plain       string
}

// TestLint_CatchesTypos 锁定：拼错的、多余的配置项会被报出来，而不是静默忽略。
//
// 曾经（且是实测过的）行为：文件里写 readtimeout（少下划线）、max_regstry（拼错）、
// totally_unknown（不存在的项），装载返回 nil，模型全部保持默认值，没有任何提示。
// 这是「改了配置没反应」最贵的来源 —— 450 个 key 的文件里错一个，肉眼找不出来。
func TestLint_CatchesTypos(t *testing.T) {
	fn := "lint_typo.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{
  "lintsec": {
    "readtimeout": 9000,
    "max_regstry": 77,
    "totally_unknown": 1,
    "plain": "ok"
  }
}`)

	cfg := newIsolated(t, fn)
	cfg.Bind("lintsec", &lintCfg{ReadTimeout: 15 * time.Second, MaxRegistry: 500})

	got := map[string]LintIssue{}
	for _, is := range cfg.Lint() {
		got[is.Path] = is
		t.Logf("  %s", is)
	}

	for _, want := range []string{"lintsec.readtimeout", "lintsec.max_regstry", "lintsec.totally_unknown"} {
		if _, ok := got[want]; !ok {
			t.Errorf("未报出未知配置项 %q", want)
		}
	}
	if _, bad := got["lintsec.plain"]; bad {
		t.Error("合法配置项被误报为未知")
	}
	if is := got["lintsec.readtimeout"]; is.Suggestion != "lintsec.read_timeout" {
		t.Errorf("readtimeout 的建议不对: got %q, want %q", is.Suggestion, "lintsec.read_timeout")
	}
	if is := got["lintsec.max_regstry"]; is.Suggestion != "lintsec.max_registry" {
		t.Errorf("max_regstry 的建议不对: got %q, want %q", is.Suggestion, "lintsec.max_registry")
	}
	if is := got["lintsec.totally_unknown"]; is.Suggestion != "" {
		t.Errorf("无关项不该给建议: got %q", is.Suggestion)
	}
	if is := got["lintsec.readtimeout"]; is.Line != 3 {
		t.Errorf("readtimeout 行号不对: got %d, want 3", is.Line)
	}
	if is := got["lintsec.max_regstry"]; is.Line != 4 {
		t.Errorf("max_regstry 行号不对: got %d, want 4", is.Line)
	}
}

// TestOrigin_TracksValueSource 锁定：能分辨一个值来自文件、默认值还是运行期设定。
//
// 这是验证的前提：验证器必须能区分「用户显式写了 0」和「代码默认值恰好是 0」。
func TestOrigin_TracksValueSource(t *testing.T) {
	fn := "lint_origin.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{
  "lintsec": {
    "max_registry": 0
  }
}`)

	cfg := newIsolated(t, fn)
	cfg.Bind("lintsec", &lintCfg{ReadTimeout: 15 * time.Second, MaxRegistry: 500, Plain: "from_code"})

	if !cfg.IsSet("lintsec.max_registry") {
		t.Error("文件里显式写的值 IsSet 应为真")
	}
	o := cfg.Origin("lintsec.max_registry")
	if o.Source != SourceFile {
		t.Errorf("来源应为文件: got %v", o.Source)
	}
	if o.Line != 3 {
		t.Errorf("行号不对: got %d, want 3", o.Line)
	}
	if !strings.HasSuffix(o.String(), ":3") {
		t.Errorf("位置前缀应形如 xxx.json:3, got %q", o.String())
	}

	if cfg.IsSet("lintsec.plain") {
		t.Error("代码默认值 IsSet 应为假")
	}
	if s := cfg.Origin("lintsec.plain").Source; s != SourceDefault {
		t.Errorf("来源应为默认值: got %v", s)
	}

	cfg.SetValue("lintsec.plain", "set_later")
	if !cfg.IsSet("lintsec.plain") {
		t.Error("运行期设定后 IsSet 应为真")
	}
	if s := cfg.Origin("lintsec.plain").Source; s != SourceRuntime {
		t.Errorf("来源应为运行期设定: got %v", s)
	}
}

// TestLint_NoFalsePositiveOnUnboundSubsection 锁定：未绑定的子组件配置节不被误报。
//
// 配置是**惰性绑定**的：只有代码真的创建了某个组件，它的配置才进树。一个只起了 server
// 的进程，config.json 里的 client.* 无人认领；vectors 那种运行期动态装模块的系统更是
// 如此。若把这些报成「未知配置项」，实测对 kylin 的 config.json 会一次吐出 300+ 条假阳性。
func TestLint_NoFalsePositiveOnUnboundSubsection(t *testing.T) {
	fn := "lint_subsection.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{
  "regowner": {
    "value": "ok",
    "sub": {
      "port": 443,
      "whatever": 1
    }
  },
  "completely": {
    "unloaded": {
      "module_setting": true
    }
  }
}`)

	cfg := newIsolated(t, fn)
	cfg.Bind("regowner", &regParent{}) // 只绑 regowner，不绑 regowner.sub

	for _, is := range cfg.Lint() {
		t.Errorf("未绑定的子组件节被误报: %s", is)
	}

	// 但一旦该子节也绑了，它内部的错字就必须能被抓到
	cfg.Bind("regowner.sub", &regChild{})
	var found bool
	for _, is := range cfg.Lint() {
		if is.Path == "regowner.sub.whatever" {
			found = true
		} else {
			t.Errorf("多报了: %s", is)
		}
	}
	if !found {
		t.Error("子节绑定后，其内部的未知字段仍未被报出")
	}
}

// TestLint_NoFalsePositiveOnNestedStruct 锁定：结构体内的嵌套 struct 字段不被误报，
// 而嵌套 struct 里的错字要报。
func TestLint_NoFalsePositiveOnNestedStruct(t *testing.T) {
	fn := "lint_nested.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{
  "nested": {
    "title": "t",
    "child": {
      "name": "n"
    }
  }
}`)

	cfg := newIsolated(t, fn)
	cfg.Bind("nested", &NestedModel{})
	for _, is := range cfg.Lint() {
		t.Errorf("嵌套 struct 字段被误报为未知: %s", is)
	}
}

// TestLint_TypoInsideOwnedChild 锁定：拥有的子组件配置里的错字要报，且建议指向子节内。
func TestLint_TypoInsideOwnedChild(t *testing.T) {
	fn := "lint_owned_child.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{
  "svc": {
    "addr": ":1",
    "child": {
      "timout": 9
    }
  }
}`)

	cfg := newIsolated(t, fn)
	cfg.Bind("svc", &svcCfg{Child: &childCfg{Port: 1}})

	issues := cfg.Lint()
	if len(issues) != 1 || issues[0].Path != "svc.child.timout" {
		t.Fatalf("应只报 svc.child.timout: %v", issues)
	}
	if issues[0].Suggestion != "svc.child.timeout" {
		t.Errorf("建议应指向子节内的字段: %q", issues[0].Suggestion)
	}
}

// TestLint_MapFieldKeysAreNotUnknown 锁定：map 字段下的任意键不是错字。
func TestLint_MapFieldKeysAreNotUnknown(t *testing.T) {
	fn := "lint_map.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{"m":{"tags":{"anything":"goes","really":"yes"},"plain":"x"}}`)

	type m struct {
		Tags  map[string]string
		Plain string
	}
	cfg := newIsolated(t, fn)
	cfg.Bind("m", &m{})
	for _, is := range cfg.Lint() {
		t.Errorf("map 字段下的键被误报: %s", is)
	}
}

type movedModel struct {
	Value string `field:"value"`
}

type movedSub struct {
	Port int `field:"port"`
}

// TestLint_NoFalsePositiveAfterRebind 锁定：一个结构体经 Unbind/Bind 换了路径、而它
// 在旧路径下的兄弟节仍留在原地时，旧节里那些已搬走的字段不被误报。
//
// 真栈形状（kylin 2026-09-05 的启动日志）：router 注册为 router.default，其
// RegistryCacher/Selector 注册为 router.default.registry / router.default.selector；
// server 随后把 router 搬到 server.http.router。于是 router.default 下 recover /
// upload_buf / use_openapi 等十个字段整节被报成「未知配置项（不会生效）」—— 而它们
// 只是搬走了。误报一多，真正的错字就没人看了。
func TestLint_NoFalsePositiveAfterRebind(t *testing.T) {
	fn := "lint_rebind.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{
  "old": {
    "owner": {
      "value": "x",
      "typo_here": 1,
      "sub": {
        "port": 1,
        "bogus": 2
      }
    }
  },
  "new": {
    "owner": {
      "value": "x",
      "vlaue": "typo"
    }
  }
}`)

	cfg := newIsolated(t, fn)
	owner := &movedModel{}
	sub := &movedSub{}
	cfg.Bind("old.owner", owner)
	cfg.Bind("old.owner.sub", sub)

	var got []string
	for _, is := range cfg.Lint() {
		got = append(got, is.Path)
	}
	if !contains(got, "old.owner.typo_here") || !contains(got, "old.owner.sub.bogus") {
		t.Fatalf("搬家前的错字没报全: %v", got)
	}

	cfg.Unbind("old.owner")
	cfg.Bind("new.owner", owner)

	got = got[:0]
	for _, is := range cfg.Lint() {
		got = append(got, is.Path)
	}
	for _, want := range []string{"old.owner.sub.bogus", "new.owner.vlaue"} {
		if !contains(got, want) {
			t.Errorf("搬家后漏报 %s: %v", want, got)
		}
	}
	for _, p := range got {
		if p == "old.owner.value" || p == "old.owner.typo_here" {
			t.Errorf("已搬走的旧节字段被误报为未知: %s", p)
		}
	}
}
