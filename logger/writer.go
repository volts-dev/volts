package logger

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"runtime"
	"sync"
)

type (
	IWriter interface {
		Init(config string) error
		Destroy()
		//Flush()
		Write(level Level, msg string) error
	}

	// 创建新Writer类型函数接口
	IWriterType func() IWriter

	TWriterMsg struct {
		level Level
		msg   string
	}

	// Manager all object and API function
	TWriterManager struct {
		//prefix string // prefix to write at beginning of each line
		flag int // properties
		//level      int
		writer       map[string]IWriter // destination for output
		level_writer map[Level]IWriter  // destination for output
		config       *TConfig
		writerName   string       // 现在使用的Writer
		buf          bytes.Buffer // for accumulating text to write
		levelStats   [6]int64

		enableFuncCallDepth bool //report detail of the path,row
		loggerFuncCallDepth int  // 1:function 2:path

		// 异步
		asynchronous bool
		msg          chan *TWriterMsg // 消息通道
		msgPool      *sync.Pool       // 缓存池

		lock sync.Mutex // ensures atomic writes; protects the following fields
	}
)

func (self *TWriterManager) writeDown(msg string, level Level) {
	for name, wt := range self.writer {
		err := wt.Write(level, msg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "unable to Write message to adapter:%v,error:%v\n", name, err)
		}
	}
}
func (self *TWriterManager) write(level Level, msg string) error {
	if level > self.config.Level {
		return nil
	}

	if self.enableFuncCallDepth {
		_, file, line, ok := runtime.Caller(self.loggerFuncCallDepth)
		if ok {
			_, filename := path.Split(file)
			msg = fmt.Sprintf("[%s:%d] %s", filename, line, msg)
		}
	}

	msg = "[" + self.config.Prefix + "]" + msg

	// 异步执行
	if self.asynchronous {
		wm := self.msgPool.Get().(*TWriterMsg)
		wm.level = level
		wm.msg = msg
		self.msg <- wm
	} else {
		self.writeDown(msg, level)
	}

	return nil
}

// start logger chan reading.
// when chan is not empty, write logs.
func (self *TWriterManager) listen() {
	for {
		// 如果不是异步则退出监听
		if !self.asynchronous {
			return
		}

		select {
		case wm := <-self.msg:
			// using level writer first
			if wt, has := self.level_writer[wm.level]; has {
				err := wt.Write(wm.level, wm.msg)
				if err != nil {
					fmt.Println("ERROR, unable to WriteMsg:", err)
				}
			} else {
				for _, wt := range self.writer {
					err := wt.Write(wm.level, wm.msg)
					if err != nil {
						fmt.Println("ERROR, unable to WriteMsg:", err)
					}
				}
			}

		}
	}
}
