package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type lintCfg struct {
	Config
	ReadTimeout time.Duration `field:"read_timeout"`
	MaxRegistry int           `field:"max_registry"`
	Plain       string
}

func (self *lintCfg) String() string { return "lintsec" }

// TestLint_CatchesTypos 锁定：拼错的、多余的配置项会被报出来，而不是静默忽略。
//
// 曾经（且是实测过的）行为：文件里写 readtimeout（少下划线）、max_regstry（拼错）、
// totally_unknown（不存在的项），LoadToModel 返回 nil，模型全部保持默认值，
// 没有任何提示。这是「改了配置没反应」最贵的来源 —— 450 个 key 的文件里错一个，
// 肉眼找不出来。
func TestLint_CatchesTypos(t *testing.T) {
	fn := "lint_typo.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	content := `{
  "lintsec": {
    "readtimeout": 9000,
    "max_regstry": 77,
    "totally_unknown": 1,
    "plain": "ok"
  }
}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := newIsolated(t, fn)
	cfg.Register(&lintCfg{ReadTimeout: 15 * time.Second, MaxRegistry: 500})

	issues := cfg.Lint()
	got := map[string]LintIssue{}
	for _, is := range issues {
		got[is.Path] = is
		t.Logf("  %s", is)
	}

	// 三个未知项都要报出来
	for _, want := range []string{"lintsec.readtimeout", "lintsec.max_regstry", "lintsec.totally_unknown"} {
		if _, ok := got[want]; !ok {
			t.Errorf("未报出未知配置项 %q", want)
		}
	}
	// 合法项不得误报
	if _, bad := got["lintsec.plain"]; bad {
		t.Error("合法配置项被误报为未知")
	}

	// 拼写建议必须指向正确的项
	if is := got["lintsec.readtimeout"]; is.Suggestion != "lintsec.read_timeout" {
		t.Errorf("readtimeout 的建议不对: got %q, want %q", is.Suggestion, "lintsec.read_timeout")
	}
	if is := got["lintsec.max_regstry"]; is.Suggestion != "lintsec.max_registry" {
		t.Errorf("max_regstry 的建议不对: got %q, want %q", is.Suggestion, "lintsec.max_registry")
	}
	// 完全无关的项不该硬凑建议
	if is := got["lintsec.totally_unknown"]; is.Suggestion != "" {
		t.Errorf("无关项不该给建议: got %q", is.Suggestion)
	}

	// 行号必须准确 —— 没有行号的报告在 450 个 key 的文件里毫无用处
	if is := got["lintsec.readtimeout"]; is.Line != 3 {
		t.Errorf("readtimeout 行号不对: got %d, want 3", is.Line)
	}
	if is := got["lintsec.max_regstry"]; is.Line != 4 {
		t.Errorf("max_regstry 行号不对: got %d, want 4", is.Line)
	}
}

// TestOrigin_TracksValueSource 锁定：能分辨一个值来自文件、默认值还是运行期设定。
//
// 这是验证的前提：验证器必须能区分「用户显式写了 0」和「代码默认值恰好是 0」，
// 否则 `if ttl <= 0 { 报错 }` 这类规则只能靠猜。
func TestOrigin_TracksValueSource(t *testing.T) {
	fn := "lint_origin.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	content := `{
  "lintsec": {
    "max_registry": 0
  }
}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := newIsolated(t, fn)
	cfg.Register(&lintCfg{ReadTimeout: 15 * time.Second, MaxRegistry: 500, Plain: "from_code"})

	// 文件里显式写的 0：IsSet 为真，来源是文件，且带行号
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

	// 代码默认值：IsSet 为假，来源是默认值
	if cfg.IsSet("lintsec.plain") {
		t.Error("代码默认值 IsSet 应为假")
	}
	if s := cfg.Origin("lintsec.plain").Source; s != SourceDefault {
		t.Errorf("来源应为默认值: got %v", s)
	}

	// 运行期设定
	cfg.SetValue("lintsec.plain", "set_later")
	if !cfg.IsSet("lintsec.plain") {
		t.Error("运行期设定后 IsSet 应为真")
	}
	if s := cfg.Origin("lintsec.plain").Source; s != SourceRuntime {
		t.Errorf("来源应为运行期设定: got %v", s)
	}
}

// TestLint_NoFalsePositiveOnUnregisteredSubsection 锁定：未注册的子组件配置节不被误报。
//
// 配置模型是**惰性注册**的：只有代码真的创建了某个组件，它的配置才进 core。
// 一个只起了 server 的进程，config.json 里的 server.http.client.* 无人认领；
// vectors 那种运行期动态装模块的系统更是如此。若把这些报成「未知配置项」，
// 实测对 kylin 的 config.json 会一次吐出 300+ 条假阳性 —— 而一个天天误报的检查
// 等于没有检查，人会直接无视它，真正的拼写错误也跟着被无视。
func TestLint_NoFalsePositiveOnUnregisteredSubsection(t *testing.T) {
	fn := "lint_subsection.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	// 文件里既有已注册模型的字段，也有一整个未注册子组件的节
	content := `{
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
}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := newIsolated(t, fn)
	cfg.Register(&regParent{}) // 只注册 regowner，不注册 regowner.sub

	for _, is := range cfg.Lint() {
		t.Errorf("未注册的子组件节被误报: %s", is)
	}

	// 但一旦该子节的模型也注册了，它内部的错字就必须能被抓到
	cfg.Register(&regChild{})
	var found bool
	for _, is := range cfg.Lint() {
		if is.Path == "regowner.sub.whatever" {
			found = true
		} else {
			t.Errorf("多报了: %s", is)
		}
	}
	if !found {
		t.Error("子节模型注册后，其内部的未知字段仍未被报出")
	}
}

// TestLint_NoFalsePositiveOnNestedStruct 锁定：模型内的嵌套 struct 字段不被误报。
func TestLint_NoFalsePositiveOnNestedStruct(t *testing.T) {
	fn := "lint_nested.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	content := `{
  "nested": {
    "title": "t",
    "child": {
      "name": "n"
    }
  }
}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := newIsolated(t, fn)
	cfg.Register(&NestedModel{})

	for _, is := range cfg.Lint() {
		t.Errorf("嵌套 struct 字段被误报为未知: %s", is)
	}
}

type movedModel struct {
	Config
	name  string
	Value string `field:"value"`
}

func (self *movedModel) String() string { return self.name }

type movedSub struct {
	Config
	name string
	Port int `field:"port"`
}

func (self *movedSub) String() string { return self.name }

// TestLint_NoFalsePositiveAfterPrefixMove 锁定：模型经 WithConfigPrefixName 搬走自己的
// 配置节、而它在旧前缀下建的子组件仍留在原地时，旧节里那些已搬走的字段不被误报。
//
// 真栈形状（kylin 2026-09-05 的启动日志）：router 注册为 router.default，其
// RegistryCacher/Selector 注册为 router.default.registry / router.default.selector；
// server 随后把 router 搬到 server.http.router。于是 router.default 成了某个已声明
// 字段的前缀，它下面 recover / upload_buf / use_openapi 等十个字段整节被报成
// 「未知配置项（不会生效）」—— 而它们只是搬走了。server.http.broker 被复用后改挂
// server.rpc 时同样中招。误报一多，真正的错字就没人看了。
func TestLint_NoFalsePositiveAfterPrefixMove(t *testing.T) {
	fn := "lint_prefix_move.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	content := `{
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
}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := newIsolated(t, fn)
	owner := &movedModel{name: "old.owner"}
	sub := &movedSub{name: "old.owner.sub"}
	cfg.Register(owner)
	cfg.Register(sub)

	// 搬家前：old.owner 是已注册节，里面的错字必须报出来（对照组）。
	var got []string
	for _, is := range cfg.Lint() {
		got = append(got, is.Path)
	}
	if !contains(got, "old.owner.typo_here") || !contains(got, "old.owner.sub.bogus") {
		t.Fatalf("搬家前的错字没报全: %v", got)
	}

	// 搬家：与 WithConfigPrefixName 的实现逐字相同 —— 注销、改名、再注册。
	cfg.Unregister(owner)
	owner.name = "new.owner"
	cfg.Register(owner)

	got = got[:0]
	for _, is := range cfg.Lint() {
		got = append(got, is.Path)
	}
	// 子组件自己的节仍在，它内部的错字照报；新节里的错字照报。
	for _, want := range []string{"old.owner.sub.bogus", "new.owner.vlaue"} {
		if !contains(got, want) {
			t.Errorf("搬家后漏报 %s: %v", want, got)
		}
	}
	// 旧节里已搬走的字段不是错字，一个都不许报。
	for _, p := range got {
		if p == "old.owner.value" || p == "old.owner.typo_here" {
			t.Errorf("已搬走的旧节字段被误报为未知: %s", p)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
