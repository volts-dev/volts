package config

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// 配置校验。
//
// 三条设计约束，都是为了让错误能被定位：
//
//  1. **路径由框架补，不由业务代码写。** 组件不知道自己被挂在配置树的哪个位置 ——
//     transport 可能是 server.http.transport，也可能是 client.rpc.transport，取决于
//     谁拥有它。框架沿结构体递归时知道每个节点的路径，业务代码只写相对字段名。
//
//  2. **自底向上。** 子结构体先验，父的 Validate 里可以假设所有子节点已经验过且合法，
//     不必自己再去检查 self.Transport.ReadTimeout > 0。
//
//  3. **一次报全。** 450 个 key 的文件，报第一个就退出意味着改一次重启一次。

// IValidator 是配置结构体可选实现的校验接口。根节和任意深度的子结构体都可以实现。
//
// 返回的错误若由 Invalid 构造，框架会自动补上完整配置路径与文件行号；返回普通
// error 则只补节名。
type IValidator interface {
	Validate() error
}

// fieldError 记录「哪个字段错了」，路径前缀留给框架补。
type fieldError struct {
	field string
	msg   string
}

func (self *fieldError) Error() string {
	if self.field == "" {
		return self.msg
	}
	return self.field + ": " + self.msg
}

// Invalid 构造一条字段级校验错误，供配置结构体的 Validate 使用：
//
//	func (self *Config) Validate() error {
//	    if self.ReadTimeout <= 0 {
//	        return config.Invalid("read_timeout", "必须为正，当前 %v", self.ReadTimeout)
//	    }
//	    return nil
//	}
//
// 框架输出时会补成：config.json:47: server.http.transport.read_timeout: 必须为正…
//
// field 用的是配置文件里的字段名（即 field tag 名或蛇形字段名），不是 Go 字段名 ——
// 报错要指向用户能在文件里搜到的东西。
func Invalid(field, format string, args ...any) error {
	return &fieldError{field: field, msg: fmt.Sprintf(format, args...)}
}

// ValidateAll 校验树上全部已绑定的结构体，自底向上，错误一次报全。
//
// 须在配置装载完毕后调用；service.Start() 会自动调一次。
//
// ★ 名字**不能**叫 Validate：旧式模型内嵌 Config，方法会被提升上去 —— 那样每个没写
// 自己 Validate 的模型都会"自动实现" IValidator，而继承来的实现是「遍历全部模型逐个
// 校验」，于是自己调自己，栈溢出。
func (self *Config) ValidateAll() error {
	var roots []*node
	self.Core().nodes.Range(func(_ any, value any) bool {
		roots = append(roots, value.(*node))
		return true
	})

	// 根之间也自底向上：路径段数多的先验。段数相同时按字典序，保证输出稳定可 diff。
	sort.Slice(roots, func(i, j int) bool {
		di := strings.Count(roots[i].path, ".")
		dj := strings.Count(roots[j].path, ".")
		if di != dj {
			return di > dj
		}
		return roots[i].path < roots[j].path
	})

	var errs []error
	for _, n := range roots {
		rv, ok := structValueOf(n.ptr)
		if !ok {
			continue
		}
		errs = append(errs, self.validateTree(rv, n.path)...)
	}
	return errors.Join(errs...)
}

// validateTree 沿子结构体递归校验，子先父后，每条错误带上自己那一节的路径。
func (self *Config) validateTree(v reflect.Value, path string) []error {
	var errs []error
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		key := fieldKey(t.Field(i))
		if key == "" {
			continue
		}
		if sv, ok := isSection(v.Field(i)); ok && sv.IsValid() {
			errs = append(errs, self.validateTree(sv, path+"."+key)...)
		}
	}
	if v.CanAddr() {
		if val, ok := v.Addr().Interface().(IValidator); ok {
			if err := val.Validate(); err != nil {
				errs = append(errs, self.decorate(path, err)...)
			}
		}
	}
	return errs
}

// decorate 给校验错误补上完整配置路径与来源位置。
//
// 逐个展开 errors.Join 出来的子错误：一个结构体可以一次报多个字段，那些字段各自
// 需要自己的路径与行号，合并成一条消息就又变回了「定位不了」。
func (self *Config) decorate(section string, err error) []error {
	if err == nil {
		return nil
	}

	// errors.Join 的结果实现了 Unwrap() []error
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var out []error
		for _, sub := range joined.Unwrap() {
			out = append(out, self.decorate(section, sub)...)
		}
		return out
	}

	var fe *fieldError
	if errors.As(err, &fe) && fe.field != "" {
		// 路径一律小写：配置键在 viper 里本就是大小写不敏感的，文件里写的是小写，
		// 报错要指向用户能搜到的东西。
		path := strings.ToLower(section + "." + fe.field)
		if loc := self.Origin(path); loc.Source == SourceFile {
			return []error{fmt.Errorf("%s: %s: %s", loc, path, fe.msg)}
		}
		return []error{fmt.Errorf("%s: %s（来自%s）", path, fe.msg, self.Origin(path).Source)}
	}

	return []error{fmt.Errorf("%s: %w", section, err)}
}

// Validate 校验默认树上全部已绑定的结构体。
//
// 包级函数不会被内嵌提升，故这里可以用 Validate 这个名字。
func Validate() error {
	return defaultConfig.ValidateAll()
}
