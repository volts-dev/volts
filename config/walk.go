package config

import (
	"reflect"
	"strings"
	"time"

	"github.com/volts-dev/utils"
)

// 配置模型的反射走查器。
//
// 这是整个包对「一个结构体里哪些字段是配置、它们叫什么、嵌套怎么算」的**唯一**答案。
// 灌默认值、装载文件、热重载比对、校验递归、Lint 的已知键、${VAR} 展开，全部走这里。
// 此前写入端用 utils.Struct2ItfMap、读取端用 mapstructure 的 MatchName、重载又自己
// 数了一遍字段 —— 三份规则各写一遍，迟早对不上（router 的 SelectorStrategy 就曾经
// 写出 selectorstrategy 却按 selector_strategy 读回，永远对不上自己）。
//
// 规则只有四条：
//
//   - 导出字段才是配置。未导出字段是实例/内部状态，反射解码本来也碰不到它们。
//   - 接口、函数、通道类型不是配置，不管导没导出 —— Logger / Registry / Transport
//     这类实例引用因此**不需要**再打 `field:"-"`。
//   - `field:"-"` 明确排除；`field:"name"` 指定键名；没有标签时用蛇形字段名
//     （ReadTimeout -> read_timeout），读写两端同一个名字。
//   - 子结构体（值或非 nil 指针）就是子节，键名同上；time.Time 例外，它是叶子。
//     匿名内嵌不走查 —— 旧式模型内嵌的 config.Config 带着 core 指针，不是配置。

// fieldKey 返回字段在配置文件里的键名（小写）；"" 表示该字段不是配置。
func fieldKey(f reflect.StructField) string {
	if f.Anonymous || f.PkgPath != "" {
		return ""
	}
	tag := f.Tag.Get("field")
	if tag == "-" || strings.HasPrefix(tag, "-,") {
		return ""
	}
	if !isConfigType(f.Type) {
		return ""
	}
	name := strings.Split(tag, ",")[0]
	if name == "" {
		name = utils.SnakeCasedName(f.Name)
	}
	return strings.ToLower(name)
}

// isConfigType 判断一个类型能否出现在配置文件里。
func isConfigType(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Interface, reflect.Func, reflect.Chan, reflect.UnsafePointer, reflect.Invalid:
		return false
	case reflect.Ptr, reflect.Slice, reflect.Array, reflect.Map:
		return isConfigType(t.Elem())
	}
	return true
}

var timeType = reflect.TypeOf(time.Time{})

// isSection 判断一个字段值是否是子节（要递归进去的结构体）。
// 返回要递归的结构体值；nil 指针返回无效值，调用方跳过。
func isSection(fv reflect.Value) (reflect.Value, bool) {
	switch fv.Kind() {
	case reflect.Struct:
		if fv.Type() == timeType {
			return reflect.Value{}, false
		}
		return fv, true
	case reflect.Ptr:
		if fv.Type().Elem().Kind() != reflect.Struct || fv.Type().Elem() == timeType {
			return reflect.Value{}, false
		}
		if fv.IsNil() {
			return reflect.Value{}, true // 是子节，但没东西可走
		}
		return fv.Elem(), true
	}
	return reflect.Value{}, false
}

// walk 递归走查一个结构体值：每个结构体节点（含根）回调 node，每个叶子回调 leaf。
// 两个回调都可为 nil。
func walk(v reflect.Value, path string, node func(path string, sv reflect.Value), leaf func(path string, fv reflect.Value)) {
	if node != nil {
		node(path, v)
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		key := fieldKey(t.Field(i))
		if key == "" {
			continue
		}
		fv := v.Field(i)
		p := path + "." + key
		if sv, ok := isSection(fv); ok {
			if sv.IsValid() {
				walk(sv, p, node, leaf)
			}
			continue
		}
		if leaf != nil {
			leaf(p, fv)
		}
	}
}

// leafValue 把叶子转成写进配置树的值。time.Duration 统一落成毫秒整数 ——
// 与 withDurationHook 的读取约定一致，且在任意嵌套深度都成立。
// nil 指针返回 (nil, false)，调用方不写。
func leafValue(fv reflect.Value) (any, bool) {
	if fv.Kind() == reflect.Ptr && fv.IsNil() {
		return nil, false
	}
	if d, ok := fv.Interface().(time.Duration); ok {
		return d.Milliseconds(), true
	}
	return fv.Interface(), true
}

// structValueOf 校验 ptr 是非 nil 的结构体指针，返回其 Elem。
func structValueOf(ptr any) (reflect.Value, bool) {
	rv := reflect.ValueOf(ptr)
	if rv.Kind() != reflect.Ptr || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return reflect.Value{}, false
	}
	return rv.Elem(), true
}

// cloneConfig 造一个 src 的配置副本：只复制配置字段，实例字段留零值；子结构体指针
// 新分配，切片与 map 深拷贝 —— 副本随后要被解码写入，与本体共享任何底层存储都会让
// 「校验不过就不应用」形同虚设（解码已经原地改了本体的切片元素）。
func cloneConfig(src reflect.Value) reflect.Value {
	dst := reflect.New(src.Type()).Elem()
	copyConfig(dst, src)
	return dst
}

func copyConfig(dst, src reflect.Value) {
	t := src.Type()
	for i := 0; i < t.NumField(); i++ {
		if fieldKey(t.Field(i)) == "" {
			continue
		}
		sf, df := src.Field(i), dst.Field(i)
		if sv, ok := isSection(sf); ok {
			if !sv.IsValid() {
				continue
			}
			if sf.Kind() == reflect.Ptr {
				df.Set(reflect.New(sf.Type().Elem()))
				copyConfig(df.Elem(), sv)
			} else {
				copyConfig(df, sv)
			}
			continue
		}
		df.Set(deepCopyLeaf(sf))
	}
}

func deepCopyLeaf(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		reflect.Copy(out, v)
		return out
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		for _, k := range v.MapKeys() {
			out.SetMapIndex(k, v.MapIndex(k))
		}
		return out
	}
	return v
}
