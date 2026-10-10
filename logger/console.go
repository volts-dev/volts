package logger

import (
	"log"
	"os"
)

// ConsoleWriter implements LoggerInterface and writes messages to terminal.
type ConsoleWriter struct {
	log   *log.Logger
	color bool
	//Level int `json:"level"`
}

// create ConsoleWriter returning as LoggerInterface.
func NewConsoleWriter() *ConsoleWriter {
	cw := new(ConsoleWriter)
	cw.log = log.New(os.Stdout, "", log.Ldate|log.Ltime)
	cw.color = stdoutWantsColor()
	return cw
}

// stdoutWantsColor 只在 stdout 是终端时上色（NO_COLOR / FORCE_COLOR 两个通行约定可覆盖）。
//
// 此前无条件上色：systemd 下 stdout 进 journald 再转 rsyslog，每行带着 \033[97m…\033[0m
// 落进 /var/log/syslog，既占地方又让 grep 认不出行首。
func stdoutWantsColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("FORCE_COLOR") != "" {
		return true
	}
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

/*
// init console logger.
// jsonconfig like '{"level":LevelTrace}'.
func (c *ConsoleWriter) Init(jsonconfig string) error {
	if len(jsonconfig) == 0 {
		return nil
	}
	err := json.Unmarshal([]byte(jsonconfig), c)
	if err != nil {
		return err
	}
	return nil
}
*/

func (self *ConsoleWriter) Init(config string) error {
	return nil
}

// write message in console.
func (self *ConsoleWriter) Write(level Level, msg string) error {
	if self.color {
		msg = level.Color(msg)
	}
	self.log.Println(msg)
	return nil
}

// implementing method. empty.
func (self *ConsoleWriter) Destroy() {
}

// implementing method. empty.
func (self *ConsoleWriter) Flush() {
}
