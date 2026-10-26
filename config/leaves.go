package config

import (
	"reflect"
	"sort"
)

// Leaf 是一个已绑定结构体里的配置叶子与它此刻的值。
type Leaf struct {
	Path  string
	Value any // 原样的 Go 值（time.Duration 不换算）；切片与 map 是副本
}

// Leaves 返回树上全部已绑定结构体的配置叶子与当前值，按路径排序。
//
// 与 Dump 的区别：Dump 读的是配置树（viper 的层），这里读的是**结构体本身** —— 也就是
// 组件实际在用的值（文件 + 默认值 + ${VAR} 展开之后）。给要按值做判断的检查用，比如
// 「这个像路径的字段指向的地方有没有人负责备份」：只看文件会漏掉写在代码默认值里的路径。
//
// 只含**已绑定**的结构体：配置是惰性绑定的，还没被创建的组件不在里面。
func (self *Config) Leaves() []Leaf {
	core := self.Core()
	core.applyMu.Lock()
	defer core.applyMu.Unlock()
	hotMu.RLock()
	defer hotMu.RUnlock()

	// 子结构体既在父节的走查里，也可能被单独绑成根节（同一份内存）：按路径去重。
	seen := map[string]bool{}
	var out []Leaf
	core.nodes.Range(func(_ any, value any) bool {
		n := value.(*node)
		rv, ok := structValueOf(n.ptr)
		if !ok {
			return true
		}
		walk(rv, n.path, nil, func(p string, fv reflect.Value) {
			if seen[p] {
				return
			}
			seen[p] = true
			if fv.Kind() == reflect.Ptr && fv.IsNil() {
				out = append(out, Leaf{Path: p})
				return
			}
			out = append(out, Leaf{Path: p, Value: deepCopyLeaf(fv).Interface()})
		})
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Leaves 返回默认树上全部已绑定结构体的配置叶子与当前值。
func Leaves() []Leaf {
	return defaultConfig.Leaves()
}
