package logger

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

type (
	ILogger interface {
		GetLevel() Level
		SetLevel(l Level)

		Assert(cnd bool, format string, args ...interface{})

		Panicf(format string, v ...interface{})
		Fatalf(string, ...interface{})
		Dbgf(format string, v ...interface{})
		Atkf(format string, v ...interface{})
		Errf(format string, v ...interface{}) error
		Warnf(format string, v ...interface{})
		Infof(format string, v ...interface{})
		Tracef(format string, v ...interface{})

		//Panic(v ...interface{})
		Panic(v ...interface{})
		Fatal(...interface{})
		Dbg(v ...interface{})
		Atk(v ...interface{})
		Err(v ...interface{}) error
		Warn(v ...interface{})
		Info(v ...interface{})
		Trace(v ...interface{})
	}

	// Supply API to user
	TLogger struct {
		Config  *Config
		manager *TWriterManager
	}
)

var (
	loggers sync.Map
	defualt = New("volts")
)

// the output like: 2035/01/01 00:00:00 [Prefix][Action] message...
func New(Prefix string, opts ...Option) *TLogger {
	Prefix = strings.ToLower(Prefix)
	if l, ok := loggers.Load(Prefix); ok {
		return l.(*TLogger)
	}

	opts = append(opts, WithPrefix(Prefix)) //
	config := newConfig(opts...)

	//log := newLogger(opts...)
	log := &TLogger{
		Config: config,
	}
	log.manager = &TWriterManager{
		writer:              make(map[string]IWriter),
		level_writer:        make(map[Level]IWriter),
		config:              config,
		msg:                 make(chan *TWriterMsg, 10000), //10000 means the number of messages in chan.
		loggerFuncCallDepth: 2,
	}

	//go log.manager.listen()

	log.manager.writer["Console"] = NewConsoleWriter()
	log.manager.writerName = "Console"

	// 缓存池新建对象函数
	log.manager.msgPool = &sync.Pool{
		New: func() interface{} {
			return &TWriterMsg{}
		},
	}

	loggers.Store(Prefix, log)
	return log
}

// Register makes a log provide available by the provided name.
// If Register is called twice with the same name or if driver is nil,
// it panics.
func RegisterWriter(name string, aWriterCreator IWriterType) {
	name = strings.ToLower(name)
	if aWriterCreator == nil {
		panic("logs: Register provide is nil")
	}
	if _, dup := creators[name]; dup {
		panic("logs: Register called twice for provider " + name)
	}
	creators[name] = aWriterCreator
}

func Get(profix string) ILogger {
	v, ok := loggers.Load(profix)
	if ok {
		return v.(ILogger)
	}
	return nil
}

func Set(profix string, log ILogger) {
	loggers.Store(profix, log)
}

func All() []ILogger {
	var logs []ILogger
	loggers.Range(func(key, value interface{}) bool {
		logs = append(logs, value.(ILogger))
		return true
	})
	return logs
}

// 断言如果结果和条件不一致就错误
func Assert(cnd bool, format string, args ...interface{}) {
	if !cnd {
		panic(fmt.Sprintf(format, args...))
	}
}

// Returns true if the given level is at or lower the current logger level
func Lvl(level Level, log ...ILogger) bool {
	var l ILogger = defualt
	if len(log) > 0 {
		l = log[0]
	}
	return l.GetLevel() <= level
}

func Atkf(fmt string, arg ...interface{}) {
	defualt.Atkf(fmt, arg...)
}

func Info(err ...interface{}) {
	defualt.Info(err...)
}

func Infof(fmt string, arg ...interface{}) {
	defualt.Infof(fmt, arg...)
}

func Warn(err ...interface{}) {
	defualt.Warn(err...)
}

func Warnf(fmt string, arg ...interface{}) {
	defualt.Warnf(fmt, arg...)
}

func Dbg(err ...interface{}) {
	defualt.Dbg(err...)
}

func Dbgf(fmt string, arg ...interface{}) {
	defualt.Dbgf(fmt, arg...)
}

func Err(err ...interface{}) error {
	return defualt.Err(err...)
}

func Errf(fmt string, arg ...interface{}) error {
	return defualt.Errf(fmt, arg...)
}

func Panicf(format string, args ...interface{}) {
	panic(fmt.Sprintf(format, args...))
}

func Fatal(args ...interface{}) {
	defualt.Fatal(args...)
}

func Fatalf(format string, args ...interface{}) {
	defualt.Fatalf(format, args...)
}

func PanicErr(err error, title ...string) bool {
	if err != nil {
		defualt.Dbg(err)
		panic(err)
		//panic("[" + title[0] + "] " + err.Error())
	}
	return false
}

func LogErr(err error, title ...string) bool {
	if err != nil {
		//defualt.ErrorLn(err)
		if len(title) > 0 {
			defualt.Err("[" + title[0] + "] " + err.Error())
		} else {
			defualt.Err(err.Error())
		}

		return true
	}
	return false
}

// SetLogger provides a given logger creater into Logger with config string.
// config need to be correct JSON as string: {"interval":360}.
func (self *TLogger) SetWriter(name string, config string) error {
	var wt IWriter
	var has bool
	name = strings.ToLower(name)
	self.manager.lock.Lock()
	defer self.manager.lock.Unlock()

	if wt, has = self.manager.writer[name]; !has {
		if creater, has := creators[name]; has {
			wt = creater()
		} else {
			return fmt.Errorf("Logger.SetLogger: unknown creater %q (forgotten Register?)", name)
		}
	}

	err := wt.Init(config)
	if err != nil {
		fmt.Println("Logger.SetLogger: " + err.Error())
		return err
	}
	self.manager.writer[name] = wt
	self.manager.writerName = name
	return nil
}

// 设置不同等级使用不同警报方式
func (self *TLogger) SetLevelWriter(level Level, writer IWriter) {
	if level > -1 && writer != nil {
		self.manager.level_writer[level] = writer
	}
}

// remove a logger adapter in BeeLogger.
func (self *TLogger) RemoveWriter(name string) error {
	self.manager.lock.Lock()
	defer self.manager.lock.Unlock()
	if wt, has := self.manager.writer[name]; has {
		wt.Destroy()
		delete(self.manager.writer, name)
	} else {
		return fmt.Errorf("Logger.RemoveWriter: unknown writer %v (forgotten Register?)", self)
	}
	return nil
}

func (self *TLogger) GetLevel() Level {
	return self.manager.config.Level
}

func (self *TLogger) SetLevel(level Level) {
	self.manager.config.Level = level
}

// Async set the log to asynchronous and start the goroutine
func (self *TLogger) Async(aSwitch ...bool) *TLogger {
	if len(aSwitch) > 0 {
		self.manager.asynchronous = aSwitch[0]
	} else {
		self.manager.asynchronous = true
	}

	// 避免多次运行 Go 程
	if self.manager.asynchronous {
		go self.manager.listen()
	}

	return self
}

// 断言如果结果和条件不一致就错误
func (self *TLogger) Assert(condition bool, format string, args ...interface{}) {
	if condition {
		panic(fmt.Sprintf(format, args...))
	}
}

// enable log funcCallDepth
func (self *TLogger) EnableFuncCallDepth(b bool) {
	self.manager.lock.Lock()
	defer self.manager.lock.Unlock()
	self.manager.enableFuncCallDepth = b
}

// set log funcCallDepth
func (self *TLogger) SetLogFuncCallDepth(aDepth int) {
	self.manager.loggerFuncCallDepth = aDepth
}

/*
// Log EMERGENCY level message.
func (self *TLogger) Emergency(format string, v ...interface{}) {
	msg := fmt.Sprintf("[M] "+format, v...)
	self.manager.writerMsg(LevelEmergency, msg)
}

// Log ALERT level message.
func (self *TLogger) Alert(format string, v ...interface{}) {
	msg := fmt.Sprintf("[A] "+format, v...)
	self.manager.writerMsg(LevelAlert, msg)
}

// Log CRITICAL level message.
func (self *TLogger) Critical(format string, v ...interface{}) {
	msg := fmt.Sprintf("[C] "+format, v...)
	self.manager.writerMsg(LevelCritical, msg)
}
*/

// Log INFORMATIONAL level message.
func (self *TLogger) Infof(format string, v ...interface{}) {
	msg := fmt.Sprintf("[INFO] "+format, v...)
	self.manager.write(LevelInfo, msg)
}

func (self *TLogger) Panicf(format string, args ...interface{}) {
	msg := fmt.Sprintf("[PANIC] "+format, args...)
	self.manager.write(LevelAlert, msg)
	panic(msg)
}

func (self *TLogger) Fatalf(format string, args ...interface{}) {
	msg := fmt.Sprintf("[FATAL] "+format, args...)
	self.manager.write(LevelAlert, msg)
	os.Exit(1)
}

// Log WARNING level message.
func (self *TLogger) Warnf(format string, v ...interface{}) {
	msg := fmt.Sprintf("[WARM] "+format, v...)
	self.manager.write(LevelWarn, msg)
}

// Log ERROR level message.
func (self *TLogger) Errf(format string, v ...interface{}) error {
	msg := fmt.Errorf("[ERR] "+format, v...)
	self.manager.write(LevelError, msg.Error())
	return msg
}

// Log DEBUG level message.
func (self *TLogger) Dbgf(format string, v ...interface{}) {
	msg := fmt.Sprintf("[DBG] "+format, v...)
	self.manager.write(LevelDebug, msg)
}

func (self *TLogger) Tracef(format string, v ...interface{}) {
	msg := fmt.Sprintf("[TRACE] "+format, v...)
	self.manager.write(LevelTrace, msg)
}

// Log Attack level message.
func (self *TLogger) Atkf(format string, v ...interface{}) {
	msg := fmt.Sprintf("[ATK] "+format, v...)
	self.manager.write(LevelAttack, msg)
}

// Log INFORMATIONAL level message.
func (self *TLogger) Info(v ...interface{}) {
	msg := fmt.Sprint(v...)
	self.manager.write(LevelInfo, "[INFO] "+msg)
}

// Log WARNING level message.
func (self *TLogger) Warn(v ...interface{}) {
	msg := fmt.Sprint(v...)
	self.manager.write(LevelWarn, "[WARM] "+msg)
}

// Log ERROR level message.
func (self *TLogger) Err(v ...interface{}) error {
	// not print nil error message
	if v != nil && v[0] == nil {
		return nil
	}

	msg := fmt.Sprint(v...)
	self.manager.write(LevelError, "[ERR] "+msg)
	return errors.New(msg)
}

func (self *TLogger) Panic(args ...interface{}) {
	msg := fmt.Sprint(args...)
	self.manager.write(LevelWarn, "[PANIC] "+msg)
	panic(msg)
}

func (self *TLogger) Fatal(v ...interface{}) {
	msg := fmt.Sprint(v...)
	self.manager.write(LevelAlert, "[FATAL] "+msg)
	os.Exit(1)
}

// Log DEBUG level message.
func (self *TLogger) Dbg(v ...interface{}) {
	msg := fmt.Sprint(v...)
	self.manager.write(LevelDebug, "[DBG] "+msg)
}

// Log Trace level message.
func (self *TLogger) Trace(v ...interface{}) {
	msg := fmt.Sprint(v...)
	self.manager.write(LevelTrace, "[TRACE] "+msg)
}

// Log Attack level message.
func (self *TLogger) Atk(v ...interface{}) {
	msg := fmt.Sprint(v...)
	self.manager.write(LevelAttack, "[ATK] "+msg)
}

/*
// flush all chan data.
func (self *TLogger) Flush() {
	for _, l := range self.manager.writer {
		l.Flush()
	}
}

// close logger, flush all chan data and destroy all adapters in TLogger.
func (self *TLogger) Close() {
	for {
		if len(self.msg) > 0 {
			bm := <-self.msg
			for _, l := range self.outputs {
				err := l.write(bm.msg, bm.level)
				if err != nil {
					fmt.Println("ERROR, unable to write (while closing logger):", err)
				}
			}
		} else {
			break
		}
	}
	for _, l := range self.outputs {
		l.Flush()
		l.Destroy()
	}
}
*/
