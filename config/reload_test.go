package config

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type rlCfg struct {
	Timeout  time.Duration `field:"timeout"`
	Name     string        `field:"name"`
	Instance *rlDep        `field:"-"` // 实例引用，热重载绝不能碰
}

type rlDep struct{ Alive bool }

type rlValidated struct {
	Port int `field:"port"`
}

func (self *rlValidated) Validate() error {
	if self.Port <= 0 {
		return Invalid("port", "必须为正")
	}
	return nil
}

// TestReload_OnlyWritesChangedFields 锁定：热重载只写真正变化的字段。
//
// 此前每次 reload 都无差别重写每个字段 —— 哪怕改的是配置文件里别人那一节。而这些字段
// 正被请求 goroutine 直接读，每次全量重写就是一次全字段竞态。
func TestReload_OnlyWritesChangedFields(t *testing.T) {
	fn := "reload_changed.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{"rlsec":{"timeout":5000,"name":"alpha"}}`)
	cfg := newIsolated(t, fn)

	dep := &rlDep{Alive: true}
	m := &rlCfg{Instance: dep}
	cfg.Bind("rlsec", m)

	if m.Timeout != 5*time.Second || m.Name != "alpha" {
		t.Fatalf("初始装载不对: %v %q", m.Timeout, m.Name)
	}

	if changes := cfg.ReloadWithChanges(); len(changes) != 0 {
		t.Errorf("配置未变却发生了写入: %+v", changes)
	}

	rewriteAndWatch(t, cfg, path, `{"rlsec":{"timeout":5000,"name":"beta"}}`)
	changes := cfg.ReloadWithChanges()
	if len(changes) != 1 {
		t.Fatalf("应只有 1 处变更, got %+v", changes)
	}
	if changes[0].Path != "rlsec.name" || changes[0].From != "alpha" || changes[0].To != "beta" {
		t.Errorf("变更描述不对: %+v", changes[0])
	}
	if m.Name != "beta" {
		t.Errorf("变更没有被应用: %q", m.Name)
	}
	if m.Instance != dep || !m.Instance.Alive {
		t.Error("热重载清掉了 field:\"-\" 的实例引用")
	}
}

// TestReload_RejectsInvalidConfig 锁定：校验不通过时整节不应用，保持原配置。
func TestReload_RejectsInvalidConfig(t *testing.T) {
	fn := "reload_invalid.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{"rlvalid":{"port":8080}}`)
	cfg := newIsolated(t, fn)

	m := &rlValidated{Port: 80}
	cfg.Bind("rlvalid", m)
	if m.Port != 8080 {
		t.Fatalf("初始装载不对: %d", m.Port)
	}

	rewriteAndWatch(t, cfg, path, `{"rlvalid":{"port":-1}}`)
	cfg.ClearErrors()

	if changes := cfg.ReloadWithChanges(); len(changes) != 0 {
		t.Errorf("非法配置被应用了: %+v", changes)
	}
	if m.Port != 8080 {
		t.Errorf("运行中的结构体被非法配置写坏: got %d, want 8080", m.Port)
	}
	if cfg.Errors() == nil {
		t.Error("拒绝应用后没有留下任何错误记录")
	}
}

// TestReload_SkipsSectionsAbsentFromFile 锁定：文件里没有的配置节不被触碰。
func TestReload_SkipsSectionsAbsentFromFile(t *testing.T) {
	fn := "reload_absent.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{"other":{"x":1}}`)
	cfg := newIsolated(t, fn)

	m := &rlCfg{Timeout: 9 * time.Second, Name: "untouched"}
	cfg.Bind("rlsec", m)

	if changes := cfg.ReloadWithChanges(); len(changes) != 0 {
		t.Errorf("文件里没有该节却发生了写入: %+v", changes)
	}
	if m.Name != "untouched" || m.Timeout != 9*time.Second {
		t.Errorf("结构体被无谓改动: %q %v", m.Name, m.Timeout)
	}
}

// TestReload_DoesNotRegisterDecodeCopy 锁定：解码用的副本不会被登记进配置树。
func TestReload_DoesNotRegisterDecodeCopy(t *testing.T) {
	fn := "reload_nocopy.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{"rlsec":{"timeout":5000,"name":"alpha"}}`)
	cfg := newIsolated(t, fn)
	cfg.Bind("rlsec", &rlCfg{})

	before := len(cfg.Bound())
	for i := 0; i < 3; i++ {
		cfg.ReloadWithChanges()
	}
	if after := len(cfg.Bound()); after != before {
		t.Errorf("重载把解码副本登记进了配置树: %d -> %d", before, after)
	}
}

type rlTakeover struct {
	Name string `field:"name"`

	mu       sync.Mutex
	applied  int
	rejectAs error
}

func (self *rlTakeover) OnReload(next any) error {
	if self.rejectAs != nil {
		return self.rejectAs
	}
	n, ok := next.(*rlTakeover)
	if !ok {
		return errors.New("类型不符")
	}
	self.mu.Lock()
	defer self.mu.Unlock()
	self.Name = n.Name
	self.applied++
	return nil
}

// TestReload_ModelTakesOverApplication 锁定：实现 IReloadable 的结构体自行应用变更，
// 框架不再直接写它的字段。
func TestReload_ModelTakesOverApplication(t *testing.T) {
	fn := "reload_takeover.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{"rltakeover":{"name":"before"}}`)
	cfg := newIsolated(t, fn)
	m := &rlTakeover{}
	cfg.Bind("rltakeover", m)

	rewriteAndWatch(t, cfg, path, `{"rltakeover":{"name":"after"}}`)
	changes := cfg.ReloadWithChanges()

	if m.applied != 1 {
		t.Errorf("OnReload 应被调用一次, got %d", m.applied)
	}
	if m.Name != "after" {
		t.Errorf("结构体自行应用后值不对: %q", m.Name)
	}
	if len(changes) != 1 || changes[0].From != "before" || changes[0].To != "after" {
		t.Errorf("变更清单不对（应在应用前算）: %+v", changes)
	}
}

// TestReload_UnsupportedMeansUntouched 锁定：声明不支持热重载的结构体一个字段都不被写。
func TestReload_UnsupportedMeansUntouched(t *testing.T) {
	fn := "reload_unsupported.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{"rltakeover":{"name":"before"}}`)
	cfg := newIsolated(t, fn)
	m := &rlTakeover{rejectAs: ErrReloadUnsupported}
	cfg.Bind("rltakeover", m)
	m.Name = "pinned"

	rewriteAndWatch(t, cfg, path, `{"rltakeover":{"name":"after"}}`)
	changes := cfg.ReloadWithChanges()

	if m.Name != "pinned" {
		t.Errorf("声明不支持热重载的结构体被改写了: %q", m.Name)
	}
	if len(changes) != 0 {
		t.Errorf("不该报出任何变更: %+v", changes)
	}
	if cfg.Errors() != nil {
		t.Errorf("ErrReloadUnsupported 是正常声明，不该记为错误: %v", cfg.Errors())
	}
}

// TestReload_ModelRejectsChange 锁定：结构体返回其他错误时保持原配置并记录错误。
func TestReload_ModelRejectsChange(t *testing.T) {
	fn := "reload_reject.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{"rltakeover":{"name":"before"}}`)
	cfg := newIsolated(t, fn)
	m := &rlTakeover{rejectAs: errors.New("业务上不接受")}
	cfg.Bind("rltakeover", m)
	m.Name = "kept"
	cfg.ClearErrors()

	rewriteAndWatch(t, cfg, path, `{"rltakeover":{"name":"after"}}`)
	cfg.ReloadWithChanges()

	if m.Name != "kept" {
		t.Errorf("结构体拒绝了变更却仍被改写: %q", m.Name)
	}
	if err := cfg.Errors(); err == nil || !strings.Contains(err.Error(), "业务上不接受") {
		t.Errorf("拒绝原因应被记录: %v", err)
	}
}

// TestReload_UnsupportedStillReportsIgnoredChanges 锁定：声明不支持热更时，
// 被忽略的变更仍要说出来 —— 无声无息地忽略就是「改了配置没反应」。
func TestReload_UnsupportedStillReportsIgnoredChanges(t *testing.T) {
	fn := "reload_unsupported_report.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	write(t, path, `{"rltakeover":{"name":"before"}}`)
	cfg := newIsolated(t, fn)
	m := &rlTakeover{rejectAs: ErrReloadUnsupported}
	cfg.Bind("rltakeover", m)

	rewriteAndWatch(t, cfg, path, `{"rltakeover":{"name":"after"}}`)
	cfg.ReloadWithChanges()

	out := buf.String()
	if !strings.Contains(out, "rltakeover.name") {
		t.Errorf("被忽略的变更没有被报出来: %q", out)
	}
	if !strings.Contains(out, "需重启") {
		t.Errorf("提示里应说明需要重启: %q", out)
	}
}

// ---- 嵌套子结构体 ----

type rlOwner struct {
	Recover bool      `field:"recover"`
	Child   *rlChild  `field:"child"`
	Pinned  *rlPinned `field:"pinned"`
}

type rlChild struct {
	Failures int      `field:"failures"`
	Hosts    []string `field:"hosts"`
}

// rlPinned 是一个拒绝热重载的子结构体，形状仿 server.Config 里的 transport。
type rlPinned struct {
	Addr string `field:"addr"`
}

func (self *rlPinned) OnReload(next any) error { return ErrReloadUnsupported }

// TestReload_WritesNestedChildLeaves 锁定：变更落在拥有的子结构体里时，只写那个叶子，
// 不整块替换子结构体（整块替换会把子结构体里的实例字段一起冲掉）。
func TestReload_WritesNestedChildLeaves(t *testing.T) {
	fn := "reload_nested.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{"own":{"recover":true,"child":{"failures":3,"hosts":["a","b"]},"pinned":{"addr":":1"}}}`)
	cfg := newIsolated(t, fn)
	child := &rlChild{}
	m := &rlOwner{Child: child, Pinned: &rlPinned{}}
	cfg.Bind("own", m)

	rewriteAndWatch(t, cfg, path, `{"own":{"recover":true,"child":{"failures":5,"hosts":["a","b"]},"pinned":{"addr":":1"}}}`)
	changes := cfg.ReloadWithChanges()
	if len(changes) != 1 || changes[0].Path != "own.child.failures" || changes[0].To != "5" {
		t.Fatalf("应只有 own.child.failures 一处变更: %+v", changes)
	}
	if m.Child != child {
		t.Error("子结构体对象被整块替换了，实例持有的指针失效")
	}
	if child.Failures != 5 {
		t.Errorf("子结构体的叶子没有被写: %d", child.Failures)
	}
}

// TestReload_NestedChildCanRefuse 锁定：子结构体自己声明不支持热重载时，父的其余字段
// 照常热更，子的一个字段都不动。
func TestReload_NestedChildCanRefuse(t *testing.T) {
	fn := "reload_nested_refuse.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	write(t, path, `{"own":{"recover":true,"child":{"failures":3},"pinned":{"addr":":1"}}}`)
	cfg := newIsolated(t, fn)
	m := &rlOwner{Child: &rlChild{}, Pinned: &rlPinned{}}
	cfg.Bind("own", m)

	rewriteAndWatch(t, cfg, path, `{"own":{"recover":false,"child":{"failures":3},"pinned":{"addr":":2"}}}`)
	changes := cfg.ReloadWithChanges()

	if len(changes) != 1 || changes[0].Path != "own.recover" {
		t.Errorf("父的字段应照常热更、子的不该出现在变更里: %+v", changes)
	}
	if m.Recover {
		t.Error("父字段没有被应用")
	}
	if m.Pinned.Addr != ":1" {
		t.Errorf("拒绝热重载的子结构体被改写了: %q", m.Pinned.Addr)
	}
	if !strings.Contains(buf.String(), "own.pinned.addr") {
		t.Errorf("被忽略的子结构体变更没有被报出来: %q", buf.String())
	}
}

// TestReload_DecodeCopyDoesNotShareSlices 锁定：解码副本不与本体共享切片底层数组 ——
// 否则「校验不过就不应用」形同虚设，解码已经原地改了本体的元素。
func TestReload_DecodeCopyDoesNotShareSlices(t *testing.T) {
	fn := "reload_slices.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{"own":{"child":{"failures":1,"hosts":["a","b"]}}}`)
	cfg := newIsolated(t, fn)
	child := &rlChild{}
	cfg.Bind("own", &rlOwner{Child: child, Pinned: &rlPinned{}})
	hosts := child.Hosts

	// failures 非法值让整节被拒 —— 但 hosts 已经解码进副本了
	type guarded struct {
		Child *rlChild `field:"child"`
	}
	_ = guarded{}
	rewriteAndWatch(t, cfg, path, `{"own":{"child":{"failures":1,"hosts":["X","b"]}}}`)
	cfg.ReloadWithChanges()
	if hosts[0] != "a" && child.Hosts[0] != "X" {
		// 应用了才允许本体变；这里应用了，本体自然是新值
		t.Fatalf("意外状态: %v %v", hosts, child.Hosts)
	}
	// 本体的旧切片不该被原地改写
	if hosts[0] != "a" {
		t.Errorf("解码副本与本体共享了底层数组，旧切片被原地改写: %v", hosts)
	}
}
