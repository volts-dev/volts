package volts

import (
	"context"
	"time"

	"github.com/volts-dev/volts/client"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/server"
	"github.com/volts-dev/volts/transport"
)

type (
	Option func(*Config)

	Config struct {
		Client client.IClient
		Server server.IServer

		// Before and After funcs
		BeforeStart []func() error // TODO 处理服务器启动后的事物
		BeforeStop  []func() error
		AfterStart  []func() error
		AfterStop   []func() error
		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context

		// Signal 决定 Run 是否接管 SIGTERM/SIGINT/SIGQUIT 并优雅停服（注销注册记录、关传输层、
		// 跑 BeforeStop/AfterStop）。默认开；HandleSignal(false) 关掉，由调用方自己管信号。
		//
		// 这个字段 2021 年就有，却从没有人把它置成 true：Run 从不订阅信号，进程收到 SIGTERM
		// 就按 Go 的默认处理当场终止 —— 停服流程一行都没跑过。注册记录只能等租约过期（etcd
		// 90 秒），期间别的进程还会把请求发给已经停掉的节点。
		Signal bool

		// StopTimeout 是收到信号后优雅停服的时限，到点还没停完就直接退出进程。0 取 DefaultStopTimeout。
		// 要比部署脚本等旧进程退出的时间短（本机 deploy3 等 40 秒）。
		StopTimeout time.Duration

		Debug bool
	}
)

// DefaultStopTimeout 是 Config.StopTimeout 的默认值。
const DefaultStopTimeout = 30 * time.Second

func newConfig(opts ...Option) *Config {
	cfg := &Config{Signal: true}
	cfg.Init(opts...)

	if cfg.Context == nil {
		cfg.Context = context.Background()
	}

	if cfg.Server == nil {
		cfg.Server = server.New(
			//server.Name(self.Name),
			server.WithTransport(
				transport.NewHTTPTransport(),
			),
		)
	}

	if cfg.Client == nil {
		cfg.Client = client.NewRpcClient(
			/*client.WithTransport(self.Transport)*/
			// 每台 server 一个客户端：配置节 client.<server name>（client.http / client.rpc）
			client.WithName(cfg.Server.Config().Name),
		)
	}
	return cfg
}

func (self *Config) Init(opts ...Option) {
	for _, opt := range opts {
		opt(self)
	}

	if self.Debug {
		//self.Transport.Init(transport.Debug())
		if self.Client != nil {
			self.Client.Init(client.Debug())
		}
		if self.Server != nil {
			self.Server.Config().Init(server.Debug())
		}
	}
}

// Name of the service
func Name(name string) Option {
	return func(cfg *Config) {
		if cfg.Server != nil {
			cfg.Server.Config().Init(server.Name(name))
		}
	}
}

// Client to be used for service
// under debug mode the port will keep at 35999
func Debug() Option {
	return func(cfg *Config) {
		cfg.Debug = true
	}
}

// BeforeStart / AfterStart / BeforeStop / AfterStop 追加生命周期钩子（按追加顺序执行）。
// CLAUDE.md 的生命周期示例一直写着它们，字段也在，选项函数却从没有过。
func BeforeStart(fn func() error) Option {
	return func(cfg *Config) { cfg.BeforeStart = append(cfg.BeforeStart, fn) }
}

func AfterStart(fn func() error) Option {
	return func(cfg *Config) { cfg.AfterStart = append(cfg.AfterStart, fn) }
}

func BeforeStop(fn func() error) Option {
	return func(cfg *Config) { cfg.BeforeStop = append(cfg.BeforeStop, fn) }
}

func AfterStop(fn func() error) Option {
	return func(cfg *Config) { cfg.AfterStop = append(cfg.AfterStop, fn) }
}

// HandleSignal 决定 Run 是否接管 SIGTERM/SIGINT/SIGQUIT（默认 true）。
func HandleSignal(b bool) Option {
	return func(cfg *Config) {
		cfg.Signal = b
	}
}

// StopTimeout 设收到信号后优雅停服的时限（默认 DefaultStopTimeout）。
func StopTimeout(d time.Duration) Option {
	return func(cfg *Config) {
		cfg.StopTimeout = d
	}
}

// Client to be used for service
func Client(cli client.IClient) Option {
	return func(cfg *Config) {
		cfg.Client = cli
	}
}

// Server to be used for service
func Server(srv server.IServer) Option {
	return func(cfg *Config) {
		cfg.Server = srv
	}
}

// Registry sets the registry for the Server
// and the underlying components
func Registry(r registry.IRegistry) Option {
	return func(cfg *Config) {
		// Update Client and Server
		if cfg.Client != nil {
			cfg.Client.Init(client.WithRegistry(r))
		}

		if cfg.Server != nil {
			cfg.Server.Config().Init(server.WithRegistry(r))
		}
	}
}

// Transport sets the transport for the Server and client
// and the underlying components
func Transport(t transport.ITransport) Option {
	return func(cfg *Config) {
		// Update Client and Server
		if cfg.Client != nil {
			cfg.Client.Init(client.WithTransport(t))
		}

		if cfg.Server != nil {
			cfg.Server.Config().Init(server.WithTransport(t))
		}

	}
}
