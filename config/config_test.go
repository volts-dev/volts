package config

import "testing"

func TestBuildJsonConfigAndSet(t *testing.T) {
	cfg := defaultConfig
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
