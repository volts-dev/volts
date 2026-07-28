package config

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 值来源追踪。
//
// 没有来源信息，配置层的错误就只能说「read_timeout 解析失败」—— 而 kylin 的
// config.json 里 read_timeout 出现 9 次，这条消息定位不了任何东西。有了来源，
// 同一条错误变成「config.json:47: server.http.transport.read_timeout: ...」。
//
// 来源同时也是**验证的前提**：验证器必须能区分「用户显式写了 0」和「代码默认值
// 恰好是 0」，否则 `if ttl <= 0 { 报错 }` 这种规则只能靠猜。P1 取消默认值回写之后
// 这个区分才真正成立 —— 在那之前默认值会被写进文件，两者从文件上看毫无差别。

type Source int

const (
	SourceUnknown Source = iota
	SourceDefault        // 代码里写死的默认值
	SourceFile           // 配置文件
	SourceRuntime        // 运行期经 SetValue 设定
)

func (self Source) String() string {
	switch self {
	case SourceDefault:
		return "默认值"
	case SourceFile:
		return "配置文件"
	case SourceRuntime:
		return "运行期设定"
	}
	return "未知"
}

// Origin 描述一个配置值的来源。
type Origin struct {
	Source Source
	File   string // 仅 SourceFile 有意义
	Line   int    // 仅 SourceFile 有意义；0 表示行号不可得（非 JSON 格式）
}

// String 返回适合出现在错误消息开头的位置前缀，如 "config.json:47"。
func (self Origin) String() string {
	if self.Source != SourceFile || self.File == "" {
		return self.Source.String()
	}
	if self.Line > 0 {
		return filepath.Base(self.File) + ":" + itoa(self.Line)
	}
	return filepath.Base(self.File)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// Origin 返回给定配置路径的值来源。
func (self *Config) Origin(path string) Origin {
	core := self.Core()
	key := strings.ToLower(path)

	if core.fmt.isOverride(key) {
		return Origin{Source: SourceRuntime}
	}
	if core.fmt.InConfig(key) {
		o := Origin{Source: SourceFile, File: core.fmt.configFile()}
		if line, ok := core.fmt.fileLine(key); ok {
			o.Line = line
		}
		return o
	}
	if core.fmt.has(key) {
		return Origin{Source: SourceDefault}
	}
	return Origin{Source: SourceUnknown}
}

// IsSet 报告该路径是否被**显式**设定过（写在配置文件里，或运行期设定过）。
//
// 与「值非零」是两回事：代码默认值恰好为 0 时 IsSet 为 false，用户在文件里显式
// 写 0 时 IsSet 为 true。验证器要区分这两种情况必须用它，不能看值本身。
func (self *Config) IsSet(path string) bool {
	core := self.Core()
	key := strings.ToLower(path)
	return core.fmt.isOverride(key) || core.fmt.InConfig(key)
}

// ---- format 侧的来源支撑 ----

func (self *format) isOverride(key string) bool {
	self.RLock()
	defer self.RUnlock()
	_, ok := self.overrideKeys[strings.ToLower(key)]
	return ok
}

func (self *format) has(key string) bool {
	self.RLock()
	defer self.RUnlock()
	return self.v.IsSet(key)
}

func (self *format) configFile() string {
	self.RLock()
	defer self.RUnlock()
	return self.v.ConfigFileUsed()
}

// rawValue 取某个 key 的当前生效值（不做类型转换），供体检报告展示用。
func (self *format) rawValue(key string) any {
	self.RLock()
	defer self.RUnlock()
	return self.v.Get(key)
}

func (self *format) fileLine(key string) (int, bool) {
	self.RLock()
	defer self.RUnlock()
	line, ok := self.lines[strings.ToLower(key)]
	return line, ok
}

// fileKeys 返回配置文件里出现过的全部叶子路径。调用方不得持锁。
func (self *format) fileKeys() []string {
	self.RLock()
	defer self.RUnlock()
	out := make([]string, 0, len(self.lines))
	for k := range self.lines {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// indexFileLines 解析配置文件，建立「叶子路径 -> 行号」索引。
//
// 只对 JSON 生效（volts 的配置文件是 config.json）。其他格式静默跳过，退化成
// 「有来源但无行号」—— 比没有来源信息仍好得多。
//
// 之所以自己再解析一遍而不复用 viper 的结果：viper 把文件解成 map 之后行号信息
// 就没了，而行号正是定位的全部价值所在。
func (self *format) indexFileLines(path string) {
	self.Lock()
	defer self.Unlock()

	self.lines = make(map[string]int)
	if !strings.EqualFold(filepath.Ext(path), ".json") {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for k, off := range jsonLeafOffsets(data) {
		self.lines[k] = 1 + bytes.Count(data[:off], []byte("\n"))
	}
}

// jsonLeafOffsets 走一遍 JSON token 流，返回每个叶子路径对应的键名字节偏移。
//
// 数组不下钻：整个数组算一个叶子（配置里的数组都是 addrs / hosts 这类标量列表，
// 逐个下标编址对定位毫无帮助）。
func jsonLeafOffsets(data []byte) map[string]int64 {
	out := make(map[string]int64)
	dec := json.NewDecoder(bytes.NewReader(data))

	var (
		path    []string // 当前对象路径
		depth   int      // 对象嵌套深度；用于判断当前是否在某个对象内
		pending string   // 刚读到、尚未消费的键名
		pendOff int64    // 该键名的偏移
	)

	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return out // 文件本身有语法错误，交给 viper 去报
		}
		// 取 token **之后**的偏移：Token() 之前的 InputOffset 指向上一个 token 的
		// 结束位置，对跨行的 JSON 会把行号算少一行。之后的偏移落在键名的闭引号上，
		// 与键名同行。
		off := dec.InputOffset()

		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				if pending != "" {
					path = append(path, pending)
					pending = ""
				}
				depth++
			case '}':
				if depth > 0 {
					depth--
				}
				if len(path) > 0 {
					path = path[:len(path)-1]
				}
			case '[':
				// 数组整体算一个叶子，记录后跳过内部
				if pending != "" {
					out[leafPath(path, pending)] = pendOff
					pending = ""
				}
				depth := 1
				for depth > 0 {
					tk, err := dec.Token()
					if err != nil {
						return out
					}
					if d, ok := tk.(json.Delim); ok {
						switch d {
						case '[', '{':
							depth++
						case ']', '}':
							depth--
						}
					}
				}
			}

		case string:
			// 对象里「键、值」交替出现；pending 为空说明轮到键名
			if depth > 0 && pending == "" {
				pending = t
				pendOff = off
				continue
			}
			if pending != "" {
				out[leafPath(path, pending)] = pendOff
				pending = ""
			}

		default:
			// 数字 / 布尔 / null
			if pending != "" {
				out[leafPath(path, pending)] = pendOff
				pending = ""
			}
		}
	}

	return out
}

func leafPath(path []string, key string) string {
	if len(path) == 0 {
		return strings.ToLower(key)
	}
	return strings.ToLower(strings.Join(path, ".") + "." + key)
}
