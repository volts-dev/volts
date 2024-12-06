package config

type ModeType int

const (
	CONFIG_FILE_NAME = "config.json"
	DEFAULT_PREFIX   = "volts"

	MODE_NORMAL ModeType = iota
	MODE_DEBUG

	MODULE_DIR   = "module" // # 模块文件夹名称
	STATIC_DIR   = "static"
	TEMPLATE_DIR = "template"
)

func (c ModeType) String() string {
	switch c {
	case MODE_NORMAL:
		return "Normal"
	case MODE_DEBUG:
		return "Debug"
	default:
		return "UNKNOW"
	}
}
