package config

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/go-viper/mapstructure/v2"
)

// 解码端。规则不在这里，在 walk.go —— 这里只是按那份规则逐字段走一遍。
//
// # 为什么不能把一整节交给 mapstructure
//
// mapstructure 只认标签与字段名，不认「接口 / 函数 / 通道不是配置」这一条。而本包的
// 模型里，同一个键名下**必然**同时站着两个字段：一个是拥有的子组件的配置，一个是那个
// 子组件的实例引用 —— router.Config 的
//
//	SelectorConfig *selector.Config `field:"selector"`   // 子节 router.<name>.selector
//	Selector       selector.ISelector                     // 实例，蛇形名也是 selector
//
// 就是设计如此（拥有＝字段、共享＝名字、实例引用不必打 field:"-"，见 CLAUDE.md
// 「Config: the tree is the struct graph」）。文件里的 `selector` 于是两边都命中，
// 接口那一边报 `'Selector' expected type 'selector.ISelector', got unconvertible
// type 'map[string]interface {}'`，而 mapstructure 是**整节**返回错误。
//
// 症状不是"少解一个字段"，是每个进程启动时都记下一条「装载配置失败，相关组件已退回
// 默认值」，且恒中 router.default / server.http / server.rpc —— 恰好就是仅有的三个
// 带 selector / registry / broker 子节的节。配置文件里留下的孤儿节
// （router.default.registry.*、server.http.broker.*，见迁移说明）撞的是同一处，
// 而 Lint 对孤儿节是刻意不出声的：两边合起来，就是一条谁也删不掉的启动期错误。
//
// # 报错的那半边还是好的那半边
//
// 上面那条报错只在实例字段**是 nil** 时发生。非 nil 时 mapstructure 的 decodeBasic
// 走的是另一支：它把接口里的动态值复制一份接着往里解 —— 而动态值是个指针，复制的是
// 指针，于是那一节的键被逐个写进**正在运行的那个实例**。所以同一份文件里，
// server.http.broker 报错（Broker 尚未注入，是 nil），而 server.http.registry /
// server.http.transport 悄悄地把孤儿节喂给了活着的 registry 与 transport 对象。
// 它们碰巧没有同名的导出字段，所以至今没出事 —— 这是运气，不是设计。
//
// # 还连累了热重载
//
// ReloadWithChanges 对每个节的处理是「解码到副本 → 校验 → 只写变了的叶子」，而解码
// 报错那一支是 `保持原配置; return`。副本由 cloneConfig 造，实例字段一律留 nil ——
// 正好落在上面那条报错的分支里。于是 **router.default 的热重载从来没有生效过**：
// config.WithWatcher() 开着，文件改了，日志里多一条「重载配置失败，保持原配置」，
// 而 Recover / RequestPrinter 这两个热字段（config/hot_field_guard_test.go 盯的
// 五个里的两个）纹丝不动。
//
// 顺带修好的是错误信息：以前只说得出字段名（'Selector'），现在说得出整条键路径。

// decodeStruct 把一节的解码输入 m 写进结构体 v。字段的取舍与键名完全按 fieldKey /
// isSection；m 里没有对应键的字段不动，多出来的键不管（那是 Lint 的活）。
func decodeStruct(m map[string]any, v reflect.Value, path string) error {
	var errs []error
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		key := fieldKey(t.Field(i))
		if key == "" {
			continue
		}
		raw, ok := lookupKey(m, key)
		if !ok || raw == nil {
			// 键不在这一节里，或写成了 null：保持结构体里已有的值（代码默认值，
			// 或 option 已经改过的值）。与 mapstructure 对 nil 输入的处理一致。
			continue
		}
		fv := v.Field(i)
		p := path + "." + key

		if _, isSec := isSection(fv); isSec {
			sub, ok := asStringMap(raw)
			if !ok {
				errs = append(errs, fmt.Errorf("%s: 需要一个子节（对象），文件里是 %T", p, raw))
				continue
			}
			if fv.Kind() == reflect.Ptr {
				if fv.IsNil() {
					if !fv.CanSet() {
						continue
					}
					fv.Set(reflect.New(fv.Type().Elem()))
				}
				fv = fv.Elem()
			}
			if err := decodeStruct(sub, fv, p); err != nil {
				errs = append(errs, err)
			}
			continue
		}

		if err := decodeLeaf(raw, fv); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
		}
	}
	return errors.Join(errs...)
}

// decodeLeaf 解一个叶子。仍旧走 mapstructure —— 弱类型转换、时长（毫秒整数 /
// "5s" 字符串）、逗号分隔的字符串切片这几条约定都在那几个 hook 里，两条读路径必须
// 用同一份。
func decodeLeaf(raw any, fv reflect.Value) error {
	if !fv.CanAddr() || !fv.CanSet() {
		return nil
	}
	dc := &mapstructure.DecoderConfig{
		Result:           fv.Addr().Interface(),
		WeaklyTypedInput: true,
	}
	withTagName("field")(dc)
	withMatchName()(dc)
	withDurationHook()(dc)
	dec, err := mapstructure.NewDecoder(dc)
	if err != nil {
		return err
	}
	return dec.Decode(raw)
}

// lookupKey 取键。viper 的键一律小写、fieldKey 也返回小写，大小写兜底只为
// 运行期 SetValue 写进来的键留一条退路。
func lookupKey(m map[string]any, key string) (any, bool) {
	if v, ok := m[key]; ok {
		return v, true
	}
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return nil, false
}

// asStringMap 把子节的原始值归一成 map[string]any（不同文件格式的解析结果不都是
// map[string]interface{}）。不是 map 就返回 false，由调用方报错。
func asStringMap(raw any) (map[string]any, bool) {
	if m, ok := raw.(map[string]any); ok {
		return m, true
	}
	rv := reflect.ValueOf(raw)
	if rv.Kind() != reflect.Map {
		return nil, false
	}
	out := make(map[string]any, rv.Len())
	for _, k := range rv.MapKeys() {
		out[fmt.Sprint(k.Interface())] = rv.MapIndex(k).Interface()
	}
	return out, true
}
