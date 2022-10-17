package config

import (
	"log"
	"testing"
	"time"
)

type (
	testConfig struct {
		*Config
		NameValue string `field:"name_value"`
		Expiry    time.Duration
		Mode      ModeType
	}
)

func newTestConfig() *testConfig {
	cfg := &testConfig{}

	Default().Register(cfg)
	Default().Init(
		WithFileName("config_test.json"),
		WithWatcher(),
	)
	return cfg
}

func (self *testConfig) String() string {
	return "testConfig"
}

func (self *testConfig) Load() error {
	self.LoadToModel(self)
	log.Println(self.Expiry, self.Mode)
	return nil
}

func (self *testConfig) Save(immed ...bool) error {
	self.NameValue = "123"
	return self.SaveFromModel(self, immed...)
}

func TestLoad(t *testing.T) {
	go func() {
		cfg := newTestConfig()
		cfg.Config.Load()
		//cfg.Load()
		//cfg.Save()
	}()
	<-make(chan int)
}

func TestLoadAndSave(t *testing.T) {
	cfg := Default()
	err := cfg.Load("config.json")
	if err != nil {
		t.Log(err)
	}

	cfg.SetValue("project.struct", 11)
	cfg.SetValue("project2.struct", 11)

	err = cfg.Save()
	if err != nil {
		t.Log(err)
	}
}

func TestBuildJsonConfigAndGet(t *testing.T) {
	cfg := defaultConfig
	err := cfg.Load("config.json")
	if err != nil {
		t.Log(err)
	}

	t.Log(cfg.GetInt64("project.struct", 0))
	t.Log(cfg.GetInt64("project2.struct", 1))
	err = cfg.Save()
	if err != nil {
		t.Log(err)
	}
}
