package config

import (
	"testing"
	"time"
)

type (
	testConfig struct {
		Config
		NameValue string `field:"name_value"`
		Expiry    time.Duration
		Mode      ModeType
		CanLoad   bool
		CanSave   bool
	}
)

func newTestConfig() *testConfig {
	Init(
		WithFileName("config_test.json"),
		WithWatcher(),
	)

	cfg := &testConfig{}

	// 测试空结构体Init调用修改配置
	cfg.SetValue("SetValue", "TEST")

	// 注册配置
	Register(cfg)
	return cfg
}

func (self *testConfig) String() string {
	return "testConfig"
}

func (self *testConfig) Load() error {
	self.CanLoad = true
	self.LoadToModel(self)
	return nil
}

func (self *testConfig) Save(immed ...bool) error {
	self.CanSave = true
	self.NameValue = "123"
	return self.SaveFromModel(self, immed...)
}

func TestLoad(t *testing.T) {
	go func() {
		cfg := newTestConfig()
		cfg.Config.LoadFromFile()
		cfg.Save()
	}()
	<-make(chan int)
}

func TestLoadAndSave(t *testing.T) {
	cfg := Default()
	err := cfg.LoadFromFile("config.json")
	if err != nil {
		t.Log(err)
	}

	cfg.SetValue("project.struct", 11)
	cfg.SetValue("project2.struct", 11)

	err = cfg.SaveToFile()
	if err != nil {
		t.Log(err)
	}
}

func TestBuildJsonConfigAndGet(t *testing.T) {
	cfg := defaultConfig
	err := cfg.LoadFromFile("config.json")
	if err != nil {
		t.Log(err)
	}

	t.Log(cfg.GetInt64("project.struct", 0))
	t.Log(cfg.GetInt64("project2.struct", 1))
	err = cfg.SaveToFile()
	if err != nil {
		t.Log(err)
	}
}
