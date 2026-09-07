package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件钉住 Bind 的核心语义：配置树就是结构体图。

// childCfg 是一个「被拥有」的子组件配置，形状仿 transport.Config。
type childCfg struct {
	Name    string `field:"-"`
	Port    int
	Timeout time.Duration `field:"timeout"`

	listener fmt.Stringer // 实例：未导出，天然不进配置
}

// svcCfg 是一个拥有子组件的父配置，形状仿 server.Config。
type svcCfg struct {
	Name  string `field:"-"`
	Addr  string
	Child *childCfg `field:"child"`

	Logger fmt.Stringer // 接口类型的实例引用：导出也不进配置，不需要 field:"-"
	Hook   func()       // 函数同理
}

// TestBind_OwnedChildIsASubsection 锁定：父结构体里的子结构体指针就是子节 ——
// 文件里 svc.child.* 直接解码进子结构体，路径不用任何人拼。
func TestBind_OwnedChildIsASubsection(t *testing.T) {
	fn := "bind_owned.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)
	write(t, path, `{"svc":{"addr":":9000","child":{"port":9,"timeout":2500}}}`)

	cfg := newIsolated(t, fn)
	svc := &svcCfg{Name: "http", Addr: ":8000", Child: &childCfg{Port: 1, Timeout: time.Second}}
	if err := cfg.Bind("svc", svc); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	if svc.Addr != ":9000" {
		t.Errorf("父字段没装载: %q", svc.Addr)
	}
	if svc.Child.Port != 9 || svc.Child.Timeout != 2500*time.Millisecond {
		t.Errorf("子结构体没沿路径装载: %+v", svc.Child)
	}

	// 子节在树上可见：Lint 的已知键、来源追踪都要认它
	if !contains(cfg.knownSections(), "svc.child") {
		t.Errorf("子结构体应是一个已知节: %v", cfg.knownSections())
	}
	if o := cfg.Origin("svc.child.port"); o.Source != SourceFile {
		t.Errorf("子节字段的来源应为文件: %v", o)
	}
	// 只有一个根节 —— 子结构体不单独成根
	if b := cfg.Bound(); len(b) != 1 || b[0] != "svc" {
		t.Errorf("根节应只有 svc: %v", b)
	}
}

// TestBind_AdoptsChildBoundElsewhere 锁定：一个已经自己绑成根节的子配置，被父结构体
// 持有后由父收编 —— 旧根节从树上消失，配置只认父路径下那一处。
//
// 真栈形状：transport.NewHTTPTransport() 自己绑在 transport.default，随后经
// server.WithTransport 注入 server；用户在文件里写的是 server.http.transport.*，
// 期望的就是它生效于这台 server 的 transport。
func TestBind_AdoptsChildBoundElsewhere(t *testing.T) {
	fn := "bind_adopt.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)
	write(t, path, `{"child":{"default":{"port":1}},"svc":{"child":{"port":7}}}`)

	cfg := newIsolated(t, fn)
	child := &childCfg{Name: "default", Port: 0}
	cfg.Bind("child.default", child)
	if child.Port != 1 {
		t.Fatalf("前置条件：独立绑定应先读到 child.default: %d", child.Port)
	}

	svc := &svcCfg{Name: "http", Child: child}
	cfg.Bind("svc", svc)

	if b := cfg.Bound(); len(b) != 1 || b[0] != "svc" {
		t.Errorf("被收编的子配置不该再是根节: %v", b)
	}
	if child.Port != 7 {
		t.Errorf("父路径下的配置应生效于同一个子对象: %d", child.Port)
	}
	if svc.Child != child {
		t.Error("收编不该换掉对象，父子仍应共享同一个结构体")
	}
}

// TestBind_UntaggedMultiwordFieldRoundTrips 锁定：没有 field 标签的多词字段读写同名。
//
// 曾经：写入端用 Go 字段名（viper 压成 readtimeout），读取端按蛇形名匹配 read_timeout，
// 于是 router 的 SelectorStrategy 写出一个自己再也读不回的键，而 Lint 把文件里正确的
// 那个报成未知。现在读写走同一个走查器。
func TestBind_UntaggedMultiwordFieldRoundTrips(t *testing.T) {
	fn := "bind_snake.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)
	write(t, path, `{"m":{"read_timeout":3000}}`)

	type m struct {
		ReadTimeout  time.Duration
		WriteTimeout time.Duration
	}
	cfg := newIsolated(t, fn)
	mm := &m{ReadTimeout: time.Second, WriteTimeout: 4 * time.Second}
	cfg.Bind("m", mm)

	if mm.ReadTimeout != 3*time.Second {
		t.Errorf("蛇形键没读进无标签字段: %v", mm.ReadTimeout)
	}
	known := cfg.knownKeys()
	if _, ok := known["m.write_timeout"]; !ok {
		t.Errorf("无标签字段的已知键应是蛇形: %v", known)
	}
	if _, bad := known["m.writetimeout"]; bad {
		t.Errorf("不该再有压扁的键: %v", known)
	}
	// 默认值也落在蛇形键上，且 Duration 统一按毫秒
	if got := cfg.GetDuration("m.write_timeout", 0); got != 4*time.Second {
		t.Errorf("默认值键名或单位不对: %v", got)
	}
	for _, is := range cfg.Lint() {
		t.Errorf("正确的蛇形键被误报: %s", is)
	}
}

// TestBind_InstanceFieldsAreNotConfig 锁定：接口 / 函数 / 通道类型的导出字段不是配置，
// 不需要 field:"-"。这是能把实例引用留在配置结构体里而不打 79 个标签的前提。
func TestBind_InstanceFieldsAreNotConfig(t *testing.T) {
	fn := "bind_instances.json"
	tpl := "bind_instances_dump.json"
	defer os.Remove(filepath.Join(AppPath, fn))
	defer os.Remove(filepath.Join(AppPath, tpl))

	cfg := newIsolated(t, fn)
	svc := &svcCfg{Name: "x", Addr: ":1", Child: &childCfg{Port: 2}, Hook: func() {}}
	cfg.Bind("svc", svc)

	known := cfg.knownKeys()
	want := map[string]bool{"svc.addr": true, "svc.child.port": true, "svc.child.timeout": true}
	for k := range known {
		if !want[k] {
			t.Errorf("多出一个不该是配置的键: %s", k)
		}
	}
	for k := range want {
		if _, ok := known[k]; !ok {
			t.Errorf("少了配置键: %s", k)
		}
	}

	if err := cfg.Dump(tpl); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(AppPath, tpl))
	for _, bad := range []string{"logger", "hook", "name", "listener"} {
		if strings.Contains(strings.ToLower(string(raw)), `"`+bad+`"`) {
			t.Errorf("模板里出现了实例/内部字段 %q:\n%s", bad, raw)
		}
	}
}

// TestBind_OverrideDoesNotShadowFileSiblings 锁定：运行期 SetValue 过某一节的一个键，
// 不会让同节里文件的其余键在装载时消失。
//
// viper 对前缀路径按层查找，哪一层先命中就整块返回那一层的子树 —— override 层有
// {m: {a: rt}} 时 Get("m") 只返回这一个键，文件里的 b 被遮住。client.Config.Init 曾在
// 每次 apply option 后整体 Save() 进 override 层，于是 config.json 里 client.* 那一节
// 从来没有生效过。解码现在直接取文件层再叠 override，不经 viper.Get。
func TestBind_OverrideDoesNotShadowFileSiblings(t *testing.T) {
	fn := "bind_shadow.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)
	write(t, path, `{"m":{"a":"file_a","b":"file_b"}}`)

	type m struct{ A, B string }
	cfg := newIsolated(t, fn)
	cfg.SetValue("m.a", "runtime_a") // 先于 Bind：旧实现在这种顺序下必丢 b

	mm := &m{A: "def_a", B: "def_b"}
	cfg.Bind("m", mm)

	if mm.A != "runtime_a" {
		t.Errorf("运行期设定值应盖过文件: %q", mm.A)
	}
	if mm.B != "file_b" {
		t.Errorf("同节里文件的其余键被 override 遮住了: %q", mm.B)
	}
}

// TestBind_SecondBindOfSamePathTakesOver 锁定：同一路径再绑一个同类型对象是多实例的
// 常态（每个 HttpClient 都叫 client.default），后来者接管该节。
func TestBind_SecondBindOfSamePathTakesOver(t *testing.T) {
	fn := "bind_twice.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	cfg := newIsolated(t, fn)
	first := &childCfg{Port: 1}
	second := &childCfg{Port: 2}
	cfg.Bind("c", first)
	cfg.Bind("c", second)

	if b := cfg.Bound(); len(b) != 1 {
		t.Errorf("同路径不该出现两个根节: %v", b)
	}
	write(t, path, `{"c":{"port":9}}`)
	if err := cfg.LoadFromFile(); err != nil {
		t.Fatal(err)
	}
	if second.Port != 9 || first.Port != 1 {
		t.Errorf("装载应只写接管后的对象: first=%d second=%d", first.Port, second.Port)
	}
}

// TestBind_RejectsNonStructPointer 锁定：绑一个不是结构体指针的东西是错误，且被记录。
func TestBind_RejectsNonStructPointer(t *testing.T) {
	cfg := newIsolated(t, "bind_reject.json")
	defer os.Remove(filepath.Join(AppPath, "bind_reject.json"))
	cfg.ClearErrors()

	var notPtr childCfg
	if err := cfg.Bind("x", notPtr); err == nil {
		t.Error("按值传入应报错")
	}
	if err := cfg.Bind("", &notPtr); err == nil {
		t.Error("空路径应报错")
	}
	if cfg.Errors() == nil {
		t.Error("错误应累积进 Errors()")
	}
}

// TestBind_DumpContainsNestedDefaults 锁定：子结构体的默认值也进模板，且 Duration 按毫秒。
func TestBind_DumpContainsNestedDefaults(t *testing.T) {
	fn := "bind_dump.json"
	tpl := "bind_dump_tpl.json"
	defer os.Remove(filepath.Join(AppPath, fn))
	defer os.Remove(filepath.Join(AppPath, tpl))

	cfg := newIsolated(t, fn)
	cfg.Bind("svc", &svcCfg{Addr: ":1", Child: &childCfg{Port: 2, Timeout: 1500 * time.Millisecond}})
	if err := cfg.Dump(tpl); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(AppPath, tpl))
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	svc, _ := m["svc"].(map[string]any)
	child, _ := svc["child"].(map[string]any)
	if child == nil || child["port"] != float64(2) || child["timeout"] != float64(1500) {
		t.Errorf("子结构体默认值没进模板或单位不对: %s", raw)
	}
}

type afterLoadCfg struct {
	Name  string `field:"name"`
	Child *afterLoadChild
	seen  []string
}

func (self *afterLoadCfg) AfterLoad() error {
	self.seen = append(self.seen, "parent:"+self.Name)
	return nil
}

type afterLoadChild struct {
	Port  int
	calls int
}

func (self *afterLoadChild) AfterLoad() error {
	self.calls++
	return nil
}

// TestBind_AfterLoadHookRunsPerLoad 锁定：AfterLoad 在 Bind 与每次 LoadFromFile 解码成功后
// 被调用，子先父后；热重载不走它。
func TestBind_AfterLoadHookRunsPerLoad(t *testing.T) {
	fn := "bind_afterload.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)
	write(t, path, `{"al":{"name":"one","child":{"port":1}}}`)

	cfg := newIsolated(t, fn)
	m := &afterLoadCfg{Child: &afterLoadChild{}}
	cfg.Bind("al", m)
	if len(m.seen) != 1 || m.seen[0] != "parent:one" || m.Child.calls != 1 {
		t.Fatalf("Bind 之后钩子应各跑一次且拿到文件值: %v %d", m.seen, m.Child.calls)
	}

	write(t, path, `{"al":{"name":"two","child":{"port":2}}}`)
	if err := cfg.LoadFromFile(); err != nil {
		t.Fatal(err)
	}
	if len(m.seen) != 2 || m.seen[1] != "parent:two" || m.Child.calls != 2 {
		t.Errorf("LoadFromFile 之后钩子应再跑一次: %v %d", m.seen, m.Child.calls)
	}

	rewriteAndWatch(t, cfg, path, `{"al":{"name":"three","child":{"port":3}}}`)
	cfg.ReloadWithChanges()
	if len(m.seen) != 2 || m.Child.calls != 2 {
		t.Errorf("热重载不该触发 AfterLoad: %v %d", m.seen, m.Child.calls)
	}
	if m.Name != "three" {
		t.Errorf("热重载本身仍应应用变更: %q", m.Name)
	}
}
