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
	self.Config.Load()
	return nil
}

func (self *testConfig) Save(immed ...bool) error {
	self.CanSave = true
	self.NameValue = "123"
	return self.Config.Save(immed...)
}

func TestLoad(t *testing.T) {
	cfg := newTestConfig()
	cfg.Config.LoadFromFile()
	cfg.Save()

	if !cfg.CanLoad {
		t.Errorf("Expected Load to be called")
	}
	if !cfg.CanSave {
		t.Errorf("Expected Save to be called")
	}
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

func TestCoverageSuite(t *testing.T) {
	// Covering options
	testConfModel := newTestConfig()
	cfg := New("coverageSuite", WithPrefix("testprefix"), WithConfig(testConfModel), WithNoAutoCreateFile())

	// Cover Unregister and InConfig
	cfg.Unregister(testConfModel)
	cfg.InConfig("test_int")

	// Cover Setters and Getters with existing and missing value defaults
	cfg.SetValue("test_bool", true)
	cfg.SetValue("test_string", "test")
	cfg.SetValue("test_int", int(42))
	cfg.SetValue("test_int32", int32(42))
	cfg.SetValue("test_int64", int64(42))
	cfg.SetValue("test_float64", 42.42)
	cfg.SetValue("test_time", time.Now())
	cfg.SetValue("test_duration", int64(100))
	cfg.SetValue("test_slice", []int{1, 2, 3})

	if !cfg.GetBool("test_bool", false) {
		t.Error("GetBool fail")
	}
	if !cfg.GetBool("test_bool_empty", true) {
		t.Error("GetBool empty fail")
	}

	if cfg.GetString("test_string", "def") != "test" {
		t.Error("GetString fail")
	}
	if cfg.GetString("test_string_empty", "def") != "def" {
		t.Error("GetString empty fail")
	}

	if cfg.GetInt("test_int", 1) != 42 {
		t.Error("GetInt fail")
	}
	if cfg.GetInt("test_int_empty", 1) != 1 {
		t.Error("GetInt empty fail")
	}

	if cfg.GetInt32("test_int32", 1) != 42 {
		t.Error("GetInt32 fail")
	}
	if cfg.GetInt32("test_int32_empty", 1) != 1 {
		t.Error("GetInt32 empty fail")
	}

	if cfg.GetInt64("test_int64", 1) != 42 {
		t.Error("GetInt64 fail")
	}
	if cfg.GetInt64("test_int64_empty", 1) != 1 {
		t.Error("GetInt64 empty fail")
	}

	if cfg.GetFloat64("test_float64", 1.1) != 42.42 {
		t.Error("GetFloat64 fail")
	}
	if cfg.GetFloat64("test_float64_empty", 1.1) != 1.1 {
		t.Error("GetFloat64 empty fail")
	}

	if cfg.GetDuration("test_duration", time.Second) != 100 {
		t.Error("GetDuration fail")
	}
	if cfg.GetDuration("test_duration_empty", time.Second) != time.Second {
		t.Error("GetDuration empty fail")
	}

	s := cfg.GetIntSlice("test_slice", []int{0})
	if len(s) != 3 || s[0] != 1 {
		t.Error("GetIntSlice fail")
	}
	s2 := cfg.GetIntSlice("test_slice_empty", []int{0})
	if len(s2) != 1 || s2[0] != 0 {
		t.Error("GetIntSlice empty fail")
	}

	cfg.GetTime("test_time", time.Time{})
	cfg.GetTime("test_time_empty", time.Now())

	// Cover Unmarshal and its decode hooks
	var unmarshalObj struct {
		TestInt int `field:"test_int"`
	}
	if err := cfg.xUnmarshal(&unmarshalObj); err != nil {
		t.Error(err)
	}

	var customDurationObj struct {
		CustomDur time.Duration `field:"test_duration"`
	}
	cfg.xUnmarshal(&customDurationObj)

	// Cover various types in Duration DecodeHook
	cfg.SetValue("test_duration_hook_int", int(100))
	cfg.SetValue("test_duration_hook_int32", int32(100))
	cfg.SetValue("test_duration_hook_float", float64(100))

	var durInt struct {
		CustomDur time.Duration `field:"test_duration_hook_int"`
	}
	var durInt32 struct {
		CustomDur time.Duration `field:"test_duration_hook_int32"`
	}
	var durFloat struct {
		CustomDur time.Duration `field:"test_duration_hook_float"`
	}

	cfg.xUnmarshal(&durInt)
	cfg.xUnmarshal(&durInt32)
	cfg.xUnmarshal(&durFloat)

	// Cover Format SetDefault directly
	cfg.Core().fmt.SetDefault("test_set_def", 123)
}
