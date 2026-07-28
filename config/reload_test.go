package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

type rlCfg struct {
	Config
	Timeout  time.Duration `field:"timeout"`
	Name     string        `field:"name"`
	Instance *rlDep        `field:"-"` // 实例引用，热重载绝不能碰
}

type rlDep struct{ Alive bool }

func (self *rlCfg) String() string { return "rlsec" }

type rlValidated struct {
	Config
	Port int `field:"port"`
}

func (self *rlValidated) String() string { return "rlvalid" }
func (self *rlValidated) Validate() error {
	if self.Port <= 0 {
		return Invalid("port", "必须为正")
	}
	return nil
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// rewriteAndWatch 模拟 WithWatcher 的真实路径：文件变动后 format.Watch 只做
// ReadInConfig 再回调 onChange(=Reload)，并不走 LoadFromFile —— 后者会顺带把新值
// 直接写进各模型，那样就绕过了本测试要验证的那段逻辑。
func rewriteAndWatch(t *testing.T, cfg *Config, path, content string) {
	t.Helper()
	write(t, path, content)
	if err := cfg.Core().fmt.ReadInConfig(); err != nil {
		t.Fatal(err)
	}
}

// TestReload_OnlyWritesChangedFields 锁定：热重载只写真正变化的字段。
//
// 此前每次 reload 都 UnmarshalKey 到活着的模型上，无差别重写每个 tagged 字段 ——
// 哪怕改的是配置文件里别人那一节。而这些字段正被请求 goroutine 直接读
// （router.Config.Recover 每请求一次、transport.Config.ReadTimeout 每连接一次），
// 每次全量重写就是一次全字段竞态。只写变化字段能让绝大多数 reload 变成零写入。
func TestReload_OnlyWritesChangedFields(t *testing.T) {
	fn := "reload_changed.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{"rlsec":{"timeout":5000,"name":"alpha"}}`)
	cfg := newIsolated(t, fn)

	dep := &rlDep{Alive: true}
	m := &rlCfg{Instance: dep}
	cfg.Register(m)

	if m.Timeout != 5*time.Second || m.Name != "alpha" {
		t.Fatalf("初始装载不对: %v %q", m.Timeout, m.Name)
	}

	// 没有任何变化时重载 —— 不该产生任何写入
	if changes := cfg.ReloadWithChanges(); len(changes) != 0 {
		t.Errorf("配置未变却发生了写入: %+v", changes)
	}

	// 只改一个字段
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

	// 实例引用必须原封不动 —— field:"-" 的字段在副本里是 nil，
	// 若纳入比对就会被判成「有值 -> nil」并把活着的引用清空
	if m.Instance != dep || !m.Instance.Alive {
		t.Error("热重载清掉了 field:\"-\" 的实例引用")
	}
}

// TestReload_RejectsInvalidConfig 锁定：校验不通过时整节不应用，保持原配置。
//
// 此前是直接解码到活着的模型：配置文件写错，运行中的实例当场被写坏且无法回退。
// 宁可继续跑旧配置，也不要把实例写成半新半旧。
func TestReload_RejectsInvalidConfig(t *testing.T) {
	fn := "reload_invalid.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{"rlvalid":{"port":8080}}`)
	cfg := newIsolated(t, fn)

	m := &rlValidated{Port: 80}
	cfg.Register(m)
	if m.Port != 8080 {
		t.Fatalf("初始装载不对: %d", m.Port)
	}

	// 改成一个非法值
	rewriteAndWatch(t, cfg, path, `{"rlvalid":{"port":-1}}`)
	cfg.ClearErrors()

	changes := cfg.ReloadWithChanges()
	if len(changes) != 0 {
		t.Errorf("非法配置被应用了: %+v", changes)
	}
	if m.Port != 8080 {
		t.Errorf("运行中的模型被非法配置写坏: got %d, want 8080", m.Port)
	}
	if cfg.Errors() == nil {
		t.Error("拒绝应用后没有留下任何错误记录")
	}
}

// TestReload_SkipsSectionsAbsentFromFile 锁定：文件里没有的配置节不被触碰。
//
// 没有新数据就不该去写活着的模型 —— 那纯粹是白白制造一次竞态。
func TestReload_SkipsSectionsAbsentFromFile(t *testing.T) {
	fn := "reload_absent.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{"other":{"x":1}}`)
	cfg := newIsolated(t, fn)

	m := &rlCfg{Timeout: 9 * time.Second, Name: "untouched"}
	cfg.Register(m)

	if changes := cfg.ReloadWithChanges(); len(changes) != 0 {
		t.Errorf("文件里没有该节却发生了写入: %+v", changes)
	}
	if m.Name != "untouched" || m.Timeout != 9*time.Second {
		t.Errorf("模型被无谓改动: %q %v", m.Name, m.Timeout)
	}
}

// TestReload_DoesNotRegisterDecodeCopy 锁定：解码用的副本不会被注册进配置树。
//
// 若走 model.Load()，那条路会经 LoadToModel 顺带把副本注册进去，配置树里从此
// 多一个没人持有的孤儿模型 —— 与 registry.Use 那个临时 Config 的问题同源。
func TestReload_DoesNotRegisterDecodeCopy(t *testing.T) {
	fn := "reload_nocopy.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{"rlsec":{"timeout":5000,"name":"alpha"}}`)
	cfg := newIsolated(t, fn)
	cfg.Register(&rlCfg{})

	before := len(cfg.knownSections())
	for i := 0; i < 3; i++ {
		cfg.ReloadWithChanges()
	}
	if after := len(cfg.knownSections()); after != before {
		t.Errorf("重载把解码副本注册进了配置树: %d -> %d", before, after)
	}
}
