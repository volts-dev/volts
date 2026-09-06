package config

import (
	"errors"
	"fmt"
	"log"
	"reflect"
	"strings"
	"sync"

	"github.com/volts-dev/utils"
)

// 热重载。
//
// 老实说清楚这里的边界：**这不是一个无竞态的实现，也修不成无竞态的**。
// 配置模型的字段是被业务代码直接读的（`router.Config.Recover` 每请求一次、
// `transport.Config.ReadTimeout` 每连接一次、`selector.Config.BreakerFailures`
// 每次 Select），没有任何拦截点。真正的解法是 copy-on-write + accessor 读取，
// 那要改的是所有读侧代码，不在配置层。
//
// 在那之前，这里做三件能立刻减少伤害、且不需要业务代码改动的事：
//
//  1. **解码到副本，校验通过才应用。** 此前是直接 UnmarshalKey 到活着的模型上：
//     配置文件写错一个字段，运行中的实例当场被写坏，且无法回退。
//  2. **只写真正变化的字段。** 此前每次 reload 无差别重写每个 tagged 字段 ——
//     哪怕改的是别人那一节，你的模型也被整体重写一遍。竞态窗口因此从「每次
//     reload × 全部字段」缩到「仅真正变更的字段」，绝大多数 reload 变成零写入。
//  3. **变更可观测。** 报出哪个字段从什么变成了什么，热更新不再是黑箱。
//
// 剩下的窗口是真实存在的：一个字段确实变了时，写入与并发读之间仍无同步。

// ErrReloadUnsupported 由 IReloadable.OnReload 返回，表示该配置不支持热重载。
//
// 这是**唯一能把竞态降到零**的选项：框架收到它就一个字段都不写，运行中的模型
// 完全不被触碰。适合那些本就不该热更的配置 —— 监听地址、数据库连接串、
// 会话存储后端之类，改了也得重启才生效，热重载去写它们只有坏处。
var ErrReloadUnsupported = errors.New("该配置不支持热重载")

// hotMu 把「热重载写模型字段」与「请求路径读同一字段」隔开。
//
// 写侧只有 applyChangedFields 一处（而且只在真有字段变化时才取写锁）；读侧由各包
// 自己的访问器持有读锁，见 HotRLock。
//
// 用一把进程级的锁而不是每个模型一把：reload 是罕见事件，读锁是共享的、无竞争时
// 只是一次原子加，相对一次请求（微秒级）可以忽略；而每模型一把锁没法放进
// config.Config —— 那个结构体会被按值复制（broker/memory 就从 *m.config 取字面量），
// 里面放锁会让 go vet 的 copylocks 对既有代码报警。
var hotMu sync.RWMutex

// HotRLock / HotRUnlock 括住一次「读会被热重载改写的字段」。
//
// 谁需要它：字段在**请求或连接路径上**被读，同时又没有标 `field:"-"`（也就是
// 热重载会写它）。目前这样的字段是 router.Config 的 Recover / RequestPrinter、
// selector.Config 的三个 Breaker*、transport.Config 的三个超时。
//
// 装配期的读不需要 —— 那时候还没有请求在跑，没有并发写。
//
// 刻意做成**包级函数而不是 Config 的方法**：每个配置模型都内嵌 config.Config，
// 方法会被提升到所有模型上，凭空给它们加一对看起来像锁 API 的导出方法。
//
//	func (self *Config) RecoverEnabled() bool {
//	    config.HotRLock()
//	    defer config.HotRUnlock()
//	    return self.Recover
//	}
//
// 两条使用禁忌：
//   - 不要**嵌套**取读锁。Go 的 RWMutex 读锁不可重入 —— 同一 goroutine 第二次
//     RLock 时若正好有写者在排队，就会死锁。现有调用点全是先后调用而非嵌套
//     （BreakerEnabled 返回后才进 mark，mark 里才取 breakerSettings）。
//   - IReloadable.OnReload 的实现里**不要**调它 —— 那条路径由模型自己接管同步，
// 框架不持有 hotMu，但模型若在 OnReload 里再取读锁，写侧一旦改成也取写锁就会死锁。
func HotRLock() { hotMu.RLock() }

// HotRUnlock 见 HotRLock。
func HotRUnlock() { hotMu.RUnlock() }

// IReloadable 由配置模型**可选**实现，用于接管热重载的应用方式。
//
// 不实现时，框架按默认方式原地写回发生变化的字段（见 applyChangedFields）——
// 那条路径与业务代码的并发读之间仍有竞态，配置层堵不住（读点遍布请求路径，
// 见本文件开头）。实现它之后，「怎样安全地应用一次变更」由最清楚这件事的人决定：
//
//	// 一、干脆拒绝热更，框架不碰任何字段（零竞态）
//	func (self *Config) OnReload(next config.IConfig) error {
//	    return config.ErrReloadUnsupported
//	}
//
//	// 二、自己用锁/原子操作安全应用（读侧需配合走同一把锁）
//	func (self *Config) OnReload(next config.IConfig) error {
//	    n, ok := next.(*Config)
//	    if !ok {
//	        return fmt.Errorf("类型不符: %T", next)
//	    }
//	    self.mu.Lock()
//	    defer self.mu.Unlock()
//	    self.Recover = n.Recover
//	    return nil
//	}
//
// next 是已解码并**通过校验**的新配置副本；返回其他错误表示拒绝本次变更，
// 框架保持原配置并把错误记进 config.Errors()。
type IReloadable interface {
	OnReload(next IConfig) error
}

// ReloadChange 描述一次热重载中某个字段的变化。
type ReloadChange struct {
	Path string // 完整配置路径
	From string
	To   string
}

// Reload 重新把配置装载进各模型。由 WithWatcher 的文件监听回调触发。
func (self *Config) Reload() {
	for _, c := range self.ReloadWithChanges() {
		log.Printf("config: 热更新 %s: %s -> %s", c.Path, c.From, c.To)
	}
}

// ReloadWithChanges 执行热重载并返回实际发生的变更。
func (self *Config) ReloadWithChanges() []ReloadChange {
	core := self.Core()
	var changes []ReloadChange

	// 与 Register / LoadFromFile 互斥：本函数跑在 WithWatcher 的监听协程上，
	// 那两条跑在调用方线程，三者写的是同一批模型对象。
	core.applyMu.Lock()
	defer core.applyMu.Unlock()

	core.models.Range(func(key any, value any) bool {
		model, ok := value.(IConfig)
		if !ok {
			return true
		}
		path := model.String()
		if path == "" || !core.fmt.InConfig(path) {
			// 文件里没有这一节 —— 没有任何新数据，不要去动活着的模型。
			return true
		}

		// 1. 造一个同类型副本，先继承当前值：文件里未出现的字段应保持现状，
		//    而不是被重置成零值。
		next := cloneModel(model)
		if next == nil {
			return true
		}
		copyTaggedFields(next, model)

		// 2. 解码到副本。绕开 model.Load()：那条路会走 LoadToModel，顺带把副本
		//    注册进配置树，留下一个没人持有的孤儿模型。
		if err := core.fmt.UnmarshalKey(path, next); err != nil {
			core.errorf("重载配置 %q 失败，保持原配置: %w", path, err)
			return true
		}

		// 3. 校验。没通过就整节不应用 —— 宁可继续跑旧配置，也不要把运行中的
		//    实例写成半新半旧。
		if v, ok := next.(IValidator); ok {
			if err := v.Validate(); err != nil {
				core.errorf("重载配置 %q 未通过校验，保持原配置: %w", path, err)
				return true
			}
		}

		// 4. 应用变更。模型若自己接管（IReloadable），框架不再直接写它的字段。
		if r, ok := model.(IReloadable); ok {
			// 变更清单必须在 OnReload **之前**算：模型自行应用之后，model 已经等于
			// next，那时再比对只会得到空清单。
			pending := diffFields(model, next, path)
			switch err := r.OnReload(next); {
			case err == nil:
				// 模型已自行应用；报出清单供观察
				changes = append(changes, pending...)
			case errors.Is(err, ErrReloadUnsupported):
				// 明确声明不支持热更 —— 一个字段都不写，这是零竞态的那条路。
				//
				// 但必须告诉人他改了什么被忽略了，否则「改了配置没反应」又回来了 ——
				// 那正是这一整轮工作在消灭的东西。不支持热更是合理的（监听地址之类
				// 改了本就要重启），无声无息地忽略不是。
				for _, c := range pending {
					log.Printf("config: %s 已在配置文件中改为 %s（原 %s），但该配置不支持热重载，需重启进程才生效",
						c.Path, c.To, c.From)
				}
			default:
				core.errorf("配置 %q 拒绝本次热重载，保持原配置: %w", path, err)
			}
			return true
		}

		// 5. 默认路径：只写真正变化的字段
		changes = append(changes, applyChangedFields(model, next, path)...)
		return true
	})

	return changes
}

// cloneModel 造一个与 model 同类型的零值实例。
func cloneModel(model IConfig) IConfig {
	rv := reflect.ValueOf(model)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return nil
	}
	if rv.Elem().Kind() != reflect.Struct {
		return nil
	}
	next, ok := reflect.New(rv.Elem().Type()).Interface().(IConfig)
	if !ok {
		return nil
	}
	return next
}

// reloadableFields 列出参与热重载比对的字段下标。
//
// 排除三类：匿名内嵌（内嵌的 config.Config 带着 core / model 指针，复制过去会让
// 副本和本体纠缠在一起）、不可导出字段、以及 `field:"-"`。
//
// 最后一类尤其要紧：`field:"-"` 标的全是实例引用（Logger / Context / Registry /
// Transport / Router…）。UnmarshalKey 不会碰它们，副本里因此是 nil；若把它们纳入
// 比对，每次 reload 都会判定「有值 -> nil」是一处变更，然后把活着的实例引用清成
// nil —— 服务当场失去 registry 和 transport。
func reloadableFields(t reflect.Type) []int {
	var out []int
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous || f.PkgPath != "" {
			continue
		}
		if tag := f.Tag.Get("field"); tag == "-" || strings.HasPrefix(tag, "-,") {
			continue
		}
		out = append(out, i)
	}
	return out
}

// copyTaggedFields 把 src 的可重载字段复制到 dst。
func copyTaggedFields(dst, src IConfig) {
	dv := reflect.ValueOf(dst).Elem()
	sv := reflect.ValueOf(src).Elem()
	if dv.Type() != sv.Type() {
		return
	}
	for _, i := range reloadableFields(dv.Type()) {
		if dv.Field(i).CanSet() {
			dv.Field(i).Set(sv.Field(i))
		}
	}
}

// diffFields 列出 next 与 cur 的字段差异，不做任何写入。
func diffFields(cur, next IConfig, path string) []ReloadChange {
	return compareFields(cur, next, path, false)
}

// applyChangedFields 把 next 中与 cur 不同的字段写回 cur，并返回变更清单。
func applyChangedFields(cur, next IConfig, path string) []ReloadChange {
	// 先只比对不写：绝大多数 reload 没有任何字段变化，那种情况下连写锁都不取，
	// 请求路径上的读锁一次都不会被阻塞。
	if len(compareFields(cur, next, path, false)) == 0 {
		return nil
	}

	// 确实要写了 —— 与请求路径上的读互斥，见 HotRLock。
	hotMu.Lock()
	defer hotMu.Unlock()
	return compareFields(cur, next, path, true)
}

func compareFields(cur, next IConfig, path string, write bool) []ReloadChange {
	cv := reflect.ValueOf(cur).Elem()
	nv := reflect.ValueOf(next).Elem()
	if cv.Type() != nv.Type() {
		return nil
	}

	var changes []ReloadChange
	for _, i := range reloadableFields(cv.Type()) {
		cf, nf := cv.Field(i), nv.Field(i)
		if !cf.CanSet() || reflect.DeepEqual(cf.Interface(), nf.Interface()) {
			continue
		}

		name := cv.Type().Field(i).Name
		if tag := cv.Type().Field(i).Tag.Get("field"); tag != "" {
			name = strings.Split(tag, ",")[0]
		} else {
			name = utils.SnakeCasedName(name)
		}

		changes = append(changes, ReloadChange{
			Path: path + "." + name,
			From: fmtValue(cf),
			To:   fmtValue(nf),
		})
		if write {
			cf.Set(nf)
		}
	}
	return changes
}

func fmtValue(v reflect.Value) string {
	if !v.CanInterface() {
		return "?"
	}
	return strings.TrimSpace(fmt.Sprint(v.Interface()))
}
