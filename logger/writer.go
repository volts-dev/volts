package logger

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"runtime"
	"sync"
	"sync/atomic"
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
	//
	// 并发约定：
	//   - writer / level_writer 两个 map 由 lock 保护（读取走 RLock）。此前
	//     writeDown 与 listen 都在裸读，与 SetWriter/RemoveWriter 的写并发时
	//     会触发 Go 运行时的 "concurrent map read and map write" 致命错误 ——
	//     那是直接终止进程，连 recover 都拦不住。
	//   - 热路径上的标量（等级、异步开关、调用深度）一律用原子量，避免每条
	//     日志都去抢一次互斥锁。
	TWriterManager struct {
		config *Config
		//prefix string // prefix to write at beginning of each line
		flag int // properties
		//level      int
		writer       map[string]IWriter // destination for output
		level_writer map[Level]IWriter  // destination for output
		writerName   string             // 现在使用的Writer
		buf          bytes.Buffer       // for accumulating text to write
		levelStats   [6]int64

		// level 是 config.Level 的原子镜像，供热路径无锁读取。
		// SetLevel 会同时更新两者；配置文件在运行期改 Level 不会同步到这里
		// （此前直接裸读 config.Level，本就是竞态，并非新增限制）。
		level atomic.Int32

		enableFuncCallDepth atomic.Bool  //report detail of the path,row
		loggerFuncCallDepth atomic.Int32 // 1:function 2:path

		// 异步
		asynchronous atomic.Bool
		msg          chan *TWriterMsg // 消息通道
		msgPool      *sync.Pool       // 缓存池
		// dropped 记录因通道满而被丢弃的日志条数。
		dropped atomic.Uint64
		// listenOnce 保证 listen goroutine 只启动一份。
		listenOnce sync.Once
		// stopOnce 保证 stop 只被关闭一次（Close 可重复调用）。
		stopOnce sync.Once
		// stop 关闭时通知 listen 退出。
		stop chan struct{}

		lock sync.RWMutex // ensures atomic writes; protects the following fields
	}
)

func (self *TWriterManager) writeDown(msg string, level Level) {
	self.lock.RLock()
	defer self.lock.RUnlock()

	for name, wt := range self.writer {
		err := wt.Write(level, msg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "unable to Write message to adapter:%v,error:%v\n", name, err)
		}
	}
}

func (self *TWriterManager) write(level Level, msg string) error {
	if int32(level) > self.level.Load() {
		return nil
	}

	// 结构化出口在排版与异步通道**之前**：它要的是原始正文，而且不能只拿到
	// 通道没丢的那一部分（见 sink.go）。
	self.notifySinks(level, msg)

	if self.enableFuncCallDepth.Load() {
		_, file, line, ok := runtime.Caller(int(self.loggerFuncCallDepth.Load()))
		if ok {
			_, filename := path.Split(file)
			msg = fmt.Sprintf("[%s:%d] %s", filename, line, msg)
		}
	}

	msg = "[" + self.config.Name + "]" + msg

	// 异步执行
	if self.asynchronous.Load() {
		wm := self.msgPool.Get().(*TWriterMsg)
		wm.level = level
		wm.msg = msg

		// 非阻塞投递：通道满时丢弃本条并计数。
		//
		// 此前是阻塞发送 —— 一旦消费端跟不上（写盘慢、writer 卡住、或者根本
		// 没人在消费），业务 goroutine 就会卡在这一行上。日志系统反向拖垮
		// 服务是绝对不能接受的：日志是可丢的，请求不是。
		select {
		case self.msg <- wm:
		default:
			self.msgPool.Put(wm)
			if self.dropped.Add(1) == 1 {
				// 只在首次丢弃时告警，避免告警本身变成新的洪水。
				fmt.Fprintf(os.Stderr,
					"[logger] 异步日志通道已满，开始丢弃日志（后续不再重复告警，用 Dropped() 查询累计条数）\n")
			}
		}
	} else {
		self.writeDown(msg, level)
	}

	return nil
}

// start logger chan reading.
// when chan is not empty, write logs.
func (self *TWriterManager) listen() {
	for {
		select {
		case <-self.stop:
			return
		case wm, ok := <-self.msg:
			if !ok {
				return
			}
			self.dispatch(wm)
			// 归还缓存对象 —— 此前只 Get 从不 Put，msgPool 等于每次都新建，
			// 白白背了一个 sync.Pool 的开销却没有任何复用。
			wm.msg = ""
			self.msgPool.Put(wm)
		}
	}
}

// dispatch 把一条消息写给对应的 writer。
// 单独成函数是为了让 RLock 的作用域收敛在一次写入内。
func (self *TWriterManager) dispatch(wm *TWriterMsg) {
	self.lock.RLock()
	defer self.lock.RUnlock()

	// using level writer first
	if wt, has := self.level_writer[wm.level]; has {
		if err := wt.Write(wm.level, wm.msg); err != nil {
			fmt.Fprintln(os.Stderr, "ERROR, unable to WriteMsg:", err)
		}
		return
	}

	for _, wt := range self.writer {
		if err := wt.Write(wm.level, wm.msg); err != nil {
			fmt.Fprintln(os.Stderr, "ERROR, unable to WriteMsg:", err)
		}
	}
}
