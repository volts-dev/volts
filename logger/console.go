package logger

import (
	"log"
	"os"
)

// ConsoleWriter implements LoggerInterface and writes messages to terminal.
type ConsoleWriter struct {
	log *log.Logger
	//Level int `json:"level"`
}

// create ConsoleWriter returning as LoggerInterface.
func NewConsoleWriter() *ConsoleWriter {
	cw := new(ConsoleWriter)
	cw.log = log.New(os.Stdout, "", log.Ldate|log.Ltime)
	return cw
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
	self.log.Println(level.Color(msg))
	return nil
}

// implementing method. empty.
func (self *ConsoleWriter) Destroy() {
}

// implementing method. empty.
func (self *ConsoleWriter) Flush() {
}
