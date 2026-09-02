package broker

import (
	"testing"

	"github.com/volts-dev/volts/config"
)

// fakeInstance 只实现 Use 会碰到的三个方法，其余靠内嵌接口占位（不会被调用）。
type fakeInstance struct {
	IBroker
	cfg *Config
}

func (f *fakeInstance) Config() *Config { return f.cfg }
func (f *fakeInstance) String() string  { return f.cfg.Name }
func (f *fakeInstance) Init(opts ...Option) error {
	f.cfg.Init(opts...)
	return nil
}

func newFakeInstance(opts ...Option) IBroker {
	cfg := &Config{Name: "http"}
	config.Detach(&cfg.Config)
	cfg.Init(opts...)
	return &fakeInstance{cfg: cfg}
}

// resetBrokerState 隔离包级全局：brokerMap / brokerHost / defaultBroker。
func resetBrokerState(t *testing.T) {
	t.Helper()

	savedMap, savedDef := brokerMap, defaultBroker
	clear := func() {
		brokerHost.Range(func(k, _ any) bool {
			brokerHost.Delete(k)
			return true
		})
	}

	clear()
	brokerMap = map[string]func(opts ...Option) IBroker{"http": newFakeInstance}
	defaultBroker = nil

	t.Cleanup(func() {
		clear()
		brokerMap, defaultBroker = savedMap, savedDef
	})
}

// 一个进程里只应有一个"这个后端"的实例：server 装配的那份和库代码 Default()
// 拿到的那份必须是同一个对象。
//
// 此前不是。server 传的 BrokerType 默认是**空串**，Use("") 在 brokerMap 里查不到，
// 掉进末尾的回落分支 `def := brokerMap["http"]; return def(opts...)` —— 直接构造，
// **完全不进实例缓存**。broker.Default() 随后又建了第二个。registry 只被注入进
// server 那份（server/config.go 的 WithRegistry），Default() 那份停在 NopRegistry，
// 它的 GetService 恒返回 (nil, nil)，于是 Publish 遍历空节点列表、一条都发不出去，
// 还返回 nil；同时它从没被 Start 过，也就没有监听端口，别人也投不进来。
// vectors 的 module/bus 拿的正是 broker.Default()。
func TestUse_ProcessWideInstanceIsShared(t *testing.T) {
	resetBrokerState(t)

	// server/config.go 的调用形态：BrokerType、BrokerHost 都是默认空串。
	srv := Use("", WithConfigPrefixName("server.http"), Addrs(""))
	if srv == nil {
		t.Fatal("Use(\"\") 返回 nil")
	}

	if got := Default(); got != srv {
		t.Errorf("broker.Default() 与 server 的 broker 不是同一个实例：%p vs %p", got, srv)
	}

	if got := Use("http"); got != srv {
		t.Errorf("Use(\"http\") 与 Use(\"\") 不是同一个实例：%p vs %p", got, srv)
	}

	// 第二个 server（vectors 的 http + rpc 两台）同样落到这一格。
	if got := Use("", WithConfigPrefixName("server.rpc"), Addrs("")); got != srv {
		t.Errorf("第二个 server 拿到了不同实例：%p vs %p", got, srv)
	}
}

// 端口 0 不是身份：server 的 WithBroker 会把 broker.Address()（未 Start 时就是
// DefaultAddress "127.0.0.1:0"）回填进 cfg.BrokerHost。这个值一旦进了 config.json，
// 下次启动就成了"带地址"的调用，不特殊处理就又分裂成两个实例。
func TestUse_ZeroPortAddressDoesNotSplitInstance(t *testing.T) {
	resetBrokerState(t)

	first := Use("")
	if first == nil {
		t.Fatal("Use(\"\") 返回 nil")
	}

	for _, addr := range []string{"127.0.0.1:0", ":0", ""} {
		if got := Use("http", Addrs(addr)); got != first {
			t.Errorf("Addrs(%q) 建出了新实例：%p vs %p", addr, got, first)
		}
	}
}

// 但真地址仍然各自独立 —— 这是既有语义，修复不该顺手改掉它。
func TestUse_DistinctAddressesStayDistinct(t *testing.T) {
	resetBrokerState(t)

	a := Use("http", Addrs("127.0.0.1:19001"))
	b := Use("http", Addrs("127.0.0.1:19002"))
	if a == nil || b == nil {
		t.Fatal("Use 返回 nil")
	}
	if a == b {
		t.Error("不同地址的 broker 被合并成了同一个实例")
	}
	if again := Use("http", Addrs("127.0.0.1:19001")); again != a {
		t.Error("同地址第二次 Use 没有复用实例")
	}
}

// 未注册的后端名回落到 http，且缓存键用回落后的真名（否则每次调用各建一个）。
func TestUse_UnknownNameFallsBackAndShares(t *testing.T) {
	resetBrokerState(t)

	first := Use("kafka")
	if first == nil {
		t.Fatal("未知后端名回落失败，返回 nil")
	}
	if got := Use("http"); got != first {
		t.Errorf("回落实例没有共用：%p vs %p", got, first)
	}
}

// SetDefault 明确指定默认实例，覆盖自动认领的结果。
func TestSetDefault_Overrides(t *testing.T) {
	resetBrokerState(t)

	first := Use("http", Addrs("127.0.0.1:19003"))
	mine := newFakeInstance()
	SetDefault(mine)

	if got := Default(); got != mine {
		t.Errorf("SetDefault 未生效：%p，期望 %p（自动认领的是 %p）", got, mine, first)
	}
}
