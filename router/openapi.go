package router

import (
	"reflect"
	"strings"

	"github.com/volts-dev/dataset"
	"github.com/volts-dev/volts/registry"
)

// Value 是 registry.Value 的别名，便于 router 内部与测试引用。
type Value = registry.Value

// reflectValue 把 Go 类型反射成 registry.Value schema 树（注册期冷路径）。
// 承接 subscriber.go extractValue，并补充 OpenAPI 所需的 tag 与防环处理。
func reflectValue(t reflect.Type) *registry.Value {
	return reflectValueVisited(t, map[reflect.Type]bool{})
}

func reflectValueVisited(t reflect.Type, seen map[reflect.Type]bool) *registry.Value {
	if t == nil {
		return nil
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	v := &registry.Value{Name: t.Name(), Type: t.Name()}

	switch t.Kind() {
	case reflect.Struct:
		if seen[t] {
			// 自引用：只放类型名，不再展开，builder 据此生成 $ref
			return &registry.Value{Name: t.Name(), Type: t.Name()}
		}
		seen[t] = true
		defer delete(seen, t)
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			if sf.PkgPath != "" {
				continue // 未导出字段跳过
			}
			fv := reflectValueVisited(sf.Type, seen)
			if fv == nil {
				continue
			}
			applyTags(sf, fv)
			v.Values = append(v.Values, fv)
		}
	case reflect.Slice, reflect.Array:
		el := t.Elem()
		for el.Kind() == reflect.Ptr {
			el = el.Elem()
		}
		v.Type = "[]" + el.Name()
		if el.Kind() == reflect.Struct {
			if inner := reflectValueVisited(t.Elem(), seen); inner != nil {
				v.Values = inner.Values
			}
		}
	case reflect.Map:
		v.Type = "object" // additionalProperties，builder 处理
		if inner := reflectValueVisited(t.Elem(), seen); inner != nil {
			v.Values = inner.Values
		}
	default:
		v.Type = t.Kind().String()
	}
	return v
}

// Operation 是一个接口的 OpenAPI 元信息，挂在 route.meta 上。
type Operation struct {
	Summary     string
	Description string
	Tags        []string
	Request     *registry.Value
	Response    *registry.Value
	// Meta 是路由的自由标记，随 RouteToEndpiont 并进 registry.Endpoint.Metadata，
	// 因而经注册中心对**所有**服务可见——这是跨进程读取路由声明的唯一通道
	// （单进程里路由表就是全局的，微服务下注册中心的 endpoints 才是）。
	// 键名由使用方自定；框架自身只占用 "description"（见 RouteToEndpiont）。
	Meta map[string]string
	// LocalOnly 为真时路由只在本进程的监听上生效，**不登记进注册中心**（见 OpLocalOnly）。
	LocalOnly bool
}

// OpOption 配置 Operation。
type OpOption func(*Operation)

func OpSummary(s string) OpOption     { return func(o *Operation) { o.Summary = s } }
func OpDescription(s string) OpOption { return func(o *Operation) { o.Description = s } }
func OpTags(tags ...string) OpOption  { return func(o *Operation) { o.Tags = append(o.Tags, tags...) } }

// OpMeta 给路由挂一条自由标记，见 Operation.Meta。
func OpMeta(key, value string) OpOption {
	return func(o *Operation) {
		if o.Meta == nil {
			o.Meta = map[string]string{}
		}
		o.Meta[key] = value
	}
}

// OpLocalOnly 标记路由只在本进程自己的监听上生效，不登记进注册中心。
//
// 登记进去的路由会被网关建成代理路由。按主机名兜底的路由（例如只在自己出口域名上才
// 有意义的根级 `/*xxx`）一旦被网关代理，就会在网关的树上与别的服务争位置 —— 根级通配
// 尤其如此：两个 `/*` 并列时只有先注册的那个生效（见 tree.go 的 catchAllRival）。
func OpLocalOnly() OpOption { return func(o *Operation) { o.LocalOnly = true } }

// buildOp 反射 I/O 类型并应用选项（注册期一次）。
func buildOp[I, O any](opts ...OpOption) *Operation {
	var i I
	var o O
	op := &Operation{
		Request:  reflectValue(reflect.TypeOf(i)),
		Response: reflectValue(reflect.TypeOf(o)),
	}
	for _, fn := range opts {
		fn(op)
	}
	return op
}

// applyTags 把结构体字段的 tag 写进字段级 Value。
func applyTags(sf reflect.StructField, fv *registry.Value) {
	// 字段名始终由结构体字段决定（json tag 优先，否则 Go 字段名），覆盖 reflectValue
	// 对标量字段用类型名（"string"/"int"…）设的默认 Name——否则无 json tag 的字段会
	// 全部塌缩成同名属性、并被 valueToSchema 误判为 object。
	name := sf.Name
	if j := sf.Tag.Get("json"); j != "" {
		if n := strings.Split(j, ",")[0]; n != "" && n != "-" {
			name = n
		}
	}
	fv.Name = name
	if sf.Tag.Get("required") == "true" {
		fv.Required = true
	}
	fv.Format = sf.Tag.Get("format")
	fv.Description = sf.Tag.Get("description")
	fv.In = sf.Tag.Get("in")
	if e := sf.Tag.Get("enum"); e != "" {
		fv.Enum = strings.Split(e, ",")
	}
}

// TypedHandler 是 OpenAPI 友好的处理器签名（context 为 IContext）。
type TypedHandler[I, O any] func(ctx IContext, in *I) (*O, error)

// noCtrl 是控制器无关注册的零大小占位 receiver：Api 方法要求 func(Ctrl, C, *I)(*O, error)
// 的方法表达式形态，而 Handle / 直接的 typed handler 没有控制器。用 noCtrl 作 Ctrl 把
// 无 receiver 的 handler 适配成方法形态，对调用方不可见（零分配）。
type noCtrl struct{}

// Handle 是 Api 的便捷形态：context 固定为 IContext。等价于 g.Api[noCtrl, IContext, I, O]
// 经 noCtrl 适配（handler 无须 receiver）。
func Handle[I, O any](g *TGroup, method, path string, h TypedHandler[I, O], opts ...OpOption) *route {
	return g.Api[noCtrl, IContext, I, O]([]string{method}, path,
		func(_ noCtrl, ctx IContext, in *I) (*O, error) { return h(ctx, in) }, opts...)
}

// bindPathQuery 用 path 参数（HTTP/RPC 皆有）和 query/form（仅 HTTP）填充 in 的字段。
// body 已由 Body().Decode 处理。为避免覆盖 body 解码出的值：
//   - in:"query"/"header" 字段只从 MethodParams 取；
//   - in:"path" 或未标注字段只从 PathParams 取，且仅当该参数确实存在（IsValid）时才赋值。
//
// 未标注且无同名 path 参数的字段（即纯 body 字段）保持 Decode 的结果不被触碰。
func bindPathQuery(ctx IContext, ptr any) {
	rv := reflect.ValueOf(ptr).Elem()
	rt := rv.Type()
	if rt.Kind() != reflect.Struct {
		return
	}
	pp := ctx.PathParams()
	var mp *TParamsSet
	if hc, ok := ctx.(*THttpContext); ok {
		mp = hc.MethodParams()
	}
	for i := 0; i < rt.NumField(); i++ {
		sf := rt.Field(i)
		fv := rv.Field(i)
		if !fv.CanSet() {
			continue
		}
		name := sf.Tag.Get("json")
		if idx := strings.IndexByte(name, ','); idx >= 0 {
			name = name[:idx]
		}
		if name == "" || name == "-" {
			name = sf.Name
		}
		var src *TParamsSet
		switch sf.Tag.Get("in") {
		case "query", "header":
			src = mp
		default: // path 或未标注
			src = pp
		}
		fs := presentField(src, name)
		if fs == nil {
			continue
		}
		setField(fv, fs)
	}
}

// presentField 返回参数集中确实存在的字段，否则 nil（防止用空值覆盖 body 字段）。
func presentField(ps *TParamsSet, name string) *dataset.TFieldSet {
	// 空参数集（fieldsIndex 未初始化）时 FieldByName 会误报 IsValid=true，
	// 会用零值覆盖 body 解码出的字段；先用 IsEmpty 挡住。
	if ps == nil || ps.IsEmpty() {
		return nil
	}
	fs := ps.FieldByName(name)
	if fs == nil || !fs.IsValid {
		return nil
	}
	return fs
}

func setField(fv reflect.Value, fs *dataset.TFieldSet) {
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(fs.AsString())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		fv.SetInt(fs.AsInteger())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		fv.SetUint(uint64(fs.AsInteger()))
	case reflect.Float32, reflect.Float64:
		fv.SetFloat(fs.AsFloat())
	case reflect.Bool:
		fv.SetBool(fs.AsBoolean())
	}
}

// writeError 把 handler 返回的 error 映射成响应。实现了 StatusCode() 的错误用其状态码，否则 500。
func writeError(ctx IContext, err error) {
	code := 500
	if sc, ok := err.(interface{ StatusCode() int }); ok {
		code = sc.StatusCode()
	}
	if hc, ok := ctx.(*THttpContext); ok {
		hc.Abort(err.Error(), code)
		return
	}
	ctx.RespondByJson(map[string]string{"error": err.Error()})
}
