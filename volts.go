package volts

import (
	"errors"
	"os"
	"os/signal"
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
	if err := config.Errors(); err != nil {
		log.Warnf("配置装载期存在错误，相关组件已退回默认值：\n%v", err)
	}

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
