package volts

import (
	"errors"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/volts-dev/volts/client"
	"github.com/volts-dev/volts/config"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/server"
)

var log = logger.New("Volts")

type (

	// Service is an interface that wraps the lower level libraries
	// within volts. Its a convenience method for building
	// and initialising services.
	IService interface {
		// The service name
		Name() string
		// Config returns the current options
		Config() *Config
		// Client is used to call services
		Client() client.IClient
		// Server is for handling requests and events
		Server() server.IServer
		// Run the service
		Run() error
		//
		Stop() error
		// The service implementation
		String() string
	}

	service struct {
		config *Config
	}
)

// NewService creates and returns a new Service based on the packages within.
func New(opts ...Option) IService {
	return &service{
		config: newConfig(opts...),
	}
}

func (self *service) Name() string {
	return self.config.Server.Name()
}

func (self *service) Config() *Config {
	return self.config
}

// configDiagnosticsOnce 保证配置诊断在一个进程里只报一次。
//
// 一个进程通常起多个 service（kylin 就同时起 HttpServer 与 RpcServer），每个都会
// 走到这里。诊断针对的是**整个进程共享的那一份配置**，报第二遍不带来任何新信息，
// 只会让同样几十行告警在启动日志里出现两次 —— 真实启动日志里已经这样了。
var configDiagnosticsOnce sync.Once

func (self *service) Start() error {
	// 组件已装配完毕，此时把配置装载期累积的错误汇总报一次。
	//
	// config 层不再 log.Fatal（库代码不该替调用方决定退进程），错误改为累积；
	// 但若无人来取，就等于把 os.Exit 换成了静默 —— 反而更糟。这里是唯一知道
	// 「所有组件都注册完了」的位置，故由它负责汇总。
	//
	// 默认只告警不拒绝启动：坏的往往是某一节业务配置，该组件会退回默认值，
	// 而整个服务仍然可用。要严格拒绝启动的场景，自行在 BeforeStart 里查
	// config.Errors() 并返回错误。
	configDiagnosticsOnce.Do(self.reportConfigDiagnostics)

	for _, fn := range self.config.BeforeStart {
		if err := fn(); err != nil {
			return err
		}
	}

	if err := self.config.Server.Start(); err != nil {
		return err
	}

	for _, fn := range self.config.AfterStart {
		if err := fn(); err != nil {
			return err
		}
	}

	return nil
}

// reportConfigDiagnostics 汇总报告配置的装载错误、未知项与校验结果。
func (self *service) reportConfigDiagnostics() {
	if err := config.Errors(); err != nil {
		log.Warnf("配置装载期存在错误，相关组件已退回默认值：\n%v", err)
	}

	// 同一时机报未知配置项：拼错的 key 会被静默忽略，配置改了不生效且毫无提示，
	// 这是排查成本最高的一类问题。差集的正确性依赖「所有模型都已注册」，而这里
	// 正是唯一能确定这一点的位置。
	if issues := config.Lint(); len(issues) > 0 {
		var b strings.Builder
		b.WriteString("配置文件存在未知配置项（这些项不会生效）：")
		for _, is := range issues {
			b.WriteString("\n  ")
			b.WriteString(is.String())
		}
		log.Warn(b.String())
	}

	// 配置校验：装载成功不等于配置合理（端口为 0、超时为负、地址为空都能装进去）。
	// 同样只告警不拒绝启动；要严格拒绝的在 BeforeStart 里查 config.Validate()。
	if err := config.Validate(); err != nil {
		log.Warnf("配置校验未通过：\n%v", err)
	}

	// 配置体检报告：设 VOLTS_CONFIG_REPORT=1 时打印一次。
	//
	// 做成环境变量触发而非常规输出，是因为它专报 Lint 不敢报的东西（整节孤儿、
	// 同构节、取值分歧），其中相当一部分在惰性注册下是正常现象 —— 天天打印会变成
	// 噪音。而做成进程内触发而非离线工具，是因为离线工具不知道业务侧注册了哪些
	// 配置模型，只能把整个业务配置报成「无人认领」。
	if os.Getenv("VOLTS_CONFIG_REPORT") != "" {
		log.Info("\n" + config.Inspect().String())
	}
}

func (self *service) Stop() error {
	var errs []error

	for _, fn := range self.config.BeforeStop {
		if err := fn(); err != nil {
			errs = append(errs, err)
		}
	}

	if err := self.config.Server.Stop(); err != nil {
		errs = append(errs, err)
	}

	for _, fn := range self.config.AfterStop {
		if err := fn(); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

func (self *service) Run() error {
	/*
		// register the debug handler
		self.Config.Server.Handle(
			self.Config.Server.NewHandler(
				handler.NewHandler(self.opts.Client),
				server.InternalHandler(true),
			),
		)

			// start the profiler
			if self.Config.Profile != nil {
				// to view mutex contention
				rtime.SetMutexProfileFraction(5)
				// to view blocking profile
				rtime.SetBlockProfileRate(1)

				if err := self.opts.Profile.Start(); err != nil {
					return err
				}
				defer self.opts.Profile.Stop()
			}
	*/

	if err := self.Start(); err != nil {
		return err
	}

	ch := make(chan os.Signal, 1)
	if self.config.Signal {
		signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)
	}

	select {
	// wait on kill signal
	case <-ch:
	// wait on context cancel
	case <-self.config.Context.Done():
	}

	return self.Stop()
}

func (self *service) Client() client.IClient {
	return self.config.Client
}

func (self *service) Server() server.IServer {
	return self.config.Server
}

func (self *service) String() string {
	if self.config.Server != nil {
		return self.config.Server.String()
	}

	return "volts service"
}
