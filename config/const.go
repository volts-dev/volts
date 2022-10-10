package config

type ModeType int

const (
	CONFIG_FILE_NAME = "config.json"
	DEFAULT_PREFIX   = "volts"

	MODE_NORMAL ModeType = iota
	MODE_DEBUG
)

func (c ModeType) String() string {
	switch c {
	case MODE_NORMAL:
		return "Normal"
	case MODE_DEBUG:
		return "Debug"
	}

	return "UNKNOW"
}
