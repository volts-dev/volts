package config

import (
	"fmt"
	"log"
	"reflect"
	"strings"

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

		// 4. 只写真正变化的字段
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

// applyChangedFields 把 next 中与 cur 不同的字段写回 cur，并返回变更清单。
func applyChangedFields(cur, next IConfig, path string) []ReloadChange {
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
		cf.Set(nf)
	}
	return changes
}

func fmtValue(v reflect.Value) string {
	if !v.CanInterface() {
		return "?"
	}
	return strings.TrimSpace(fmt.Sprint(v.Interface()))
}
