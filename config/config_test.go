package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCoverageSuite 覆盖 Get/Set 各类型与解码钩子。
func TestCoverageSuite(t *testing.T) {
	fn := "coverage_suite.json"
	defer os.Remove(filepath.Join(AppPath, fn))
	cfg := newIsolated(t, fn)

	cfg.InConfig("test_int")

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
	// 裸数字按毫秒 —— 与写入端的 d.Milliseconds() 及 withDurationHook 一致。
	if cfg.GetDuration("test_duration", time.Second) != 100*time.Millisecond {
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

	// 解码钩子：各类整数按毫秒解成 Duration
	var unmarshalObj struct {
		TestInt int `field:"test_int"`
	}
	if err := cfg.Core().fmt.Unmarshal(&unmarshalObj); err != nil {
		t.Error(err)
	}
	if unmarshalObj.TestInt != 42 {
		t.Errorf("Unmarshal: %d", unmarshalObj.TestInt)
	}

	cfg.SetValue("test_duration_hook_int", int(100))
	cfg.SetValue("test_duration_hook_int32", int32(100))
	cfg.SetValue("test_duration_hook_float", float64(100))
	var durs struct {
		A time.Duration `field:"test_duration_hook_int"`
		B time.Duration `field:"test_duration_hook_int32"`
		C time.Duration `field:"test_duration_hook_float"`
		D time.Duration `field:"test_duration"`
	}
	if err := cfg.Core().fmt.Unmarshal(&durs); err != nil {
		t.Error(err)
	}
	for name, d := range map[string]time.Duration{"int": durs.A, "int32": durs.B, "float": durs.C, "int64": durs.D} {
		if d != 100*time.Millisecond {
			t.Errorf("Duration 钩子对 %s 解错: %v", name, d)
		}
	}

	cfg.Core().fmt.SetDefault("test_set_def", 123)
	if cfg.GetInt("test_set_def", 0) != 123 {
		t.Error("SetDefault fail")
	}
}
