package config

import (
	"errors"
	"fmt"
	"log"
	"reflect"
	"strings"
	"sync"
)

// 热重载。
//
// 每个根节：解码到一份**副本**，校验通过才应用，只写真正变化的字段，写入时持 hotMu
// 与请求路径上的读互斥。子结构体沿字段递归，子节可以自己实现 IReloadable 接管或拒绝。
//
//  1. **解码到副本。** 直接解码到活着的结构体上，配置文件写错一个字段，运行中的实例
//     当场被写坏且无法回退。
//  2. **只写真正变化的字段。** 无差别重写每个字段等于每次 reload 一次全字段竞态；
//     只写变化的，绝大多数 reload 变成零写入，连写锁都不取。
//  3. **变更可观测。** 报出哪个字段从什么变成了什么。

// ErrReloadUnsupported 由 IReloadable.OnReload 返回，表示该配置不支持热重载。
//
// 这是**唯一能把竞态降到零**的选项：框架收到它就一个字段都不写，运行中的结构体
// 完全不被触碰。适合那些本就不该热更的配置 —— 监听地址、数据库连接串之类，改了也得
// 重启才生效，热重载去写它们只有坏处。
var ErrReloadUnsupported = errors.New("该配置不支持热重载")

// hotMu 把「热重载写结构体字段」与「请求路径读同一字段」隔开。
//
// 写侧只有 applyChanges 一处（而且只在真有字段变化时才取写锁）；读侧由各包自己的
// 访问器持有读锁，见 HotRLock。
//
// 用一把进程级的锁而不是每个结构体一把：reload 是罕见事件，读锁是共享的、无竞争时
// 只是一次原子加，相对一次请求（微秒级）可以忽略；而配置结构体会被按值复制
// （broker/memory 就从 *m.config 取字面量），里面放锁会让 go vet 的 copylocks 报警。
var hotMu sync.RWMutex

// HotRLock / HotRUnlock 括住一次「读会被热重载改写的字段」。
//
// 谁需要它：字段在**请求或连接路径上**被读，同时热重载会写它（所在的结构体没有拒绝
// 热重载）。目前这样的字段是 router.Config 的 Recover / RequestPrinter、selector.Config
// 的三个 Breaker*。装配期的读不需要 —— 那时候还没有请求在跑。
//
//	func (self *Config) RecoverEnabled() bool {
//	    config.HotRLock()
//	    defer config.HotRUnlock()
//	    return self.Recover
//	}
//
// 两条使用禁忌：
//   - 不要**嵌套**取读锁。Go 的 RWMutex 读锁不可重入 —— 同一 goroutine 第二次 RLock
//     时若正好有写者在排队，就会死锁。
//   - IReloadable.OnReload 的实现里**不要**调它 —— OnReload 在写锁之内被调用。
func HotRLock() { hotMu.RLock() }

// HotRUnlock 见 HotRLock。
func HotRUnlock() { hotMu.RUnlock() }

// IReloadable 由配置结构体**可选**实现，用于接管热重载的应用方式。根节和任意深度的
// 子结构体都可以实现。
//
// 不实现时，框架按默认方式原地写回发生变化的字段。实现之后，「怎样安全地应用一次
// 变更」由最清楚这件事的人决定：
//
//	// 一、干脆拒绝热更，框架不碰这一节的任何字段（零竞态）
//	func (self *Config) OnReload(next any) error {
//	    return config.ErrReloadUnsupported
//	}
//
//	// 二、自己用锁/原子操作安全应用（读侧需配合走同一把锁）
//	func (self *Config) OnReload(next any) error {
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
// next 是与接收者同类型的指针，指向已解码并**通过校验**的新配置副本；返回其他错误
// 表示拒绝本次变更，框架保持原配置并把错误记进 config.Errors()。
type IReloadable interface {
	OnReload(next any) error
}

// ReloadChange 描述一次热重载中某个字段的变化。
type ReloadChange struct {
	Path string // 完整配置路径
	From string
	To   string
}

// Reload 重新把配置装载进各结构体。由 WithWatcher 的文件监听回调触发。
func (self *Config) Reload() {
	for _, c := range self.ReloadWithChanges() {
		log.Printf("config: 热更新 %s: %s -> %s", c.Path, c.From, c.To)
	}
}

// ReloadWithChanges 执行热重载并返回实际发生的变更。
func (self *Config) ReloadWithChanges() []ReloadChange {
	core := self.Core()
	var changes []ReloadChange

	// 与 Bind / LoadFromFile 互斥：本函数跑在 WithWatcher 的监听协程上，
	// 那两条跑在调用方线程，三者写的是同一批对象。
	core.applyMu.Lock()
	defer core.applyMu.Unlock()

	core.nodes.Range(func(_ any, value any) bool {
		n := value.(*node)
		cur, ok := structValueOf(n.ptr)
		if !ok {
			return true
		}

		// 1. 副本先继承当前值：文件里未出现的字段应保持现状，而不是被重置成零值。
		next := cloneConfig(cur)

		// 2. 解码到副本。文件里没有这一节 —— 没有任何新数据，不要去动活着的结构体。
		found, err := core.fmt.decodeSection(n.path, next.Addr().Interface())
		if !found {
			return true
		}
		if err != nil {
			core.errorf("重载配置 %q 失败，保持原配置: %w", n.path, err)
			return true
		}

		// 3. 校验（沿子结构体递归）。没通过就整节不应用 —— 宁可继续跑旧配置，也不要
		//    把运行中的实例写成半新半旧。
		if errs := self.validateTree(next, n.path); len(errs) > 0 {
			core.errorf("重载配置 %q 未通过校验，保持原配置: %w", n.path, errors.Join(errs...))
			return true
		}

		// 4. 应用
		changes = append(changes, core.applyChanges(cur, next, n.path)...)
		return true
	})

	return changes
}

// applyChanges 把 next 中与 cur 不同的字段写回 cur，并返回变更清单。
func (self *core) applyChanges(cur, next reflect.Value, path string) []ReloadChange {
	// 先只比对不写：绝大多数 reload 没有任何字段变化，那种情况下连写锁都不取，
	// 请求路径上的读锁一次都不会被阻塞。
	if len(self.reloadStruct(cur, next, path, false)) == 0 {
		return nil
	}

	// 确实要写了 —— 与请求路径上的读互斥，见 HotRLock。
	hotMu.Lock()
	defer hotMu.Unlock()
	return self.reloadStruct(cur, next, path, true)
}

// reloadStruct 比对（write=false）或应用（write=true）一个结构体节点及其子树。
//
// 实现了 IReloadable 的节点由它自己接管整棵子树：变更清单在 OnReload **之前**算——
// 模型自行应用之后 cur 已经等于 next，那时再比对只会得到空清单。
func (self *core) reloadStruct(cur, next reflect.Value, path string, write bool) []ReloadChange {
	if cur.CanAddr() {
		if r, ok := cur.Addr().Interface().(IReloadable); ok {
			pending := diffStruct(cur, next, path)
			if !write || len(pending) == 0 {
				return pending
			}
			switch err := r.OnReload(next.Addr().Interface()); {
			case err == nil:
				return pending
			case errors.Is(err, ErrReloadUnsupported):
				// 明确声明不支持热更 —— 一个字段都不写。但必须告诉人他改了什么被忽略了，
				// 否则「改了配置没反应」又回来了。不支持热更是合理的，无声无息地忽略不是。
				for _, c := range pending {
					log.Printf("config: %s 已在配置文件中改为 %s（原 %s），但该配置不支持热重载，需重启进程才生效",
						c.Path, c.To, c.From)
				}
			default:
				self.errorf("配置 %q 拒绝本次热重载，保持原配置: %w", path, err)
			}
			return nil
		}
	}

	var changes []ReloadChange
	t := cur.Type()
	for i := 0; i < t.NumField(); i++ {
		key := fieldKey(t.Field(i))
		if key == "" {
			continue
		}
		cf, nf := cur.Field(i), next.Field(i)
		p := path + "." + key

		if cs, ok := isSection(cf); ok {
			ns, _ := isSection(nf)
			if !cs.IsValid() || !ns.IsValid() {
				continue // 一边是 nil 指针：没有可比的东西
			}
			changes = append(changes, self.reloadStruct(cs, ns, p, write)...)
			continue
		}

		if !cf.CanSet() || reflect.DeepEqual(cf.Interface(), nf.Interface()) {
			continue
		}
		changes = append(changes, ReloadChange{Path: p, From: fmtValue(cf), To: fmtValue(nf)})
		if write {
			cf.Set(nf)
		}
	}
	return changes
}

// diffStruct 列出 next 与 cur 整棵子树的叶子差异，不做任何写入、不触发任何 OnReload。
func diffStruct(cur, next reflect.Value, path string) []ReloadChange {
	var changes []ReloadChange
	t := cur.Type()
	for i := 0; i < t.NumField(); i++ {
		key := fieldKey(t.Field(i))
		if key == "" {
			continue
		}
		cf, nf := cur.Field(i), next.Field(i)
		p := path + "." + key
		if cs, ok := isSection(cf); ok {
			ns, _ := isSection(nf)
			if cs.IsValid() && ns.IsValid() {
				changes = append(changes, diffStruct(cs, ns, p)...)
			}
			continue
		}
		if !reflect.DeepEqual(cf.Interface(), nf.Interface()) {
			changes = append(changes, ReloadChange{Path: p, From: fmtValue(cf), To: fmtValue(nf)})
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
