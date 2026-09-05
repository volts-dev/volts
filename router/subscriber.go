package router

import (
	"context"
	"fmt"
	"hash/crc32"
	"reflect"
	"strings"

	"github.com/volts-dev/utils"
	"github.com/volts-dev/volts/broker"
	"github.com/volts-dev/volts/registry"
)

type (
	SubscriberOption func(*SubscriberConfig)

	SubscriberConfig struct {
		Context context.Context
		Queue   string
		// AutoAck defaults to true. When a handler returns
		// with a nil error the message is acked.
		AutoAck  bool
		Internal bool
	}
	/*
		handler struct {
			method  reflect.Value
			reqType reflect.Type
			ctxType reflect.Type
		}
	*/
	subscriber struct {
		topic      string
		rcvr       reflect.Value
		typ        reflect.Type
		subscriber interface{} // 原型 subscriber
		handlers   []*handler
		endpoints  []*registry.Endpoint
		config     SubscriberConfig
	}

	TSubscriberContext struct {
		Event broker.IEvent // 当前消息事件，处理器通过此字段读取 Topic/Message/Ack
	}
	// Subscriber interface represents a subscription to a given topic using
	// a specific subscriber function or object with endpoints. It mirrors
	// the handler in its behavior.
	ISubscriber interface {
		Topic() string
		Subscriber() interface{} // 原型
		Endpoints() []*registry.Endpoint
		Handlers() []*handler
		Config() SubscriberConfig
	}
)

// subscriberFuncType 是订阅处理器唯一可执行的签名：func(*TSubscriberContext)。
//
// `handle.SubFunc` 就是这个类型，`handler.InvokeSubscriber` 只会去调它 —— 别的形状
// 即便登记成功也永远不会被执行。
var subscriberFuncType = reflect.TypeOf(func(*TSubscriberContext) {})

// isSubscriberFunc 报告 t 是否就是 func(*TSubscriberContext)。
func isSubscriberFunc(t reflect.Type) bool {
	return t != nil && t == subscriberFuncType
}

// validateSubscriber 拒绝那些"登记得上、但永远不会被调用"的订阅者。
//
// 此前它的函数体就是 `return nil` —— 什么都不校验。而 parseSubscriber 的两条分支并不
// 对称：`func(*TSubscriberContext)` 那条会往 `hd.funcs` 里放执行体，走反射的那条
// （结构体的方法、以 reflect.Value 传进来的函数）只建了 handler 就返回，`funcs` 是空的。
// `handler.InvokeSubscriber` 遍历的正是 `funcs`，于是那类订阅者**静默地一次都不触发**：
// Subscribe 返回 nil、注册中心里有这条订阅、消息也确实投递到了本进程，就是没人处理。
//
// 反射那条分支现在会正常填 funcs（见 parseSubscriber），本函数是最后一道闸：万一还有
// 什么形状漏过去，让它在**注册期**报错，而不是上线之后表现为"这个事件没反应"。
func validateSubscriber(sub ISubscriber) error {
	if sub == nil {
		return fmt.Errorf("invalid subscriber: nil")
	}
	for _, hd := range sub.Handlers() {
		if hd == nil {
			return fmt.Errorf("invalid subscriber %q: nil handler", sub.Topic())
		}
		if len(hd.funcs) == 0 {
			return fmt.Errorf("invalid subscriber %q: 处理器 %q 没有可执行的函数体 —— "+
				"订阅处理器的签名必须是 func(*router.TSubscriberContext)；"+
				"结构体订阅者则要求同签名且是**值接收者**的方法（指针接收者不在值的方法集里，"+
				"与本框架路由层的通则一致）", sub.Topic(), hd.Name)
		}
	}
	return nil
}

func extractValue(v reflect.Type, d int) *registry.Value {
	if d == 3 {
		return nil
	}
	if v == nil {
		return nil
	}

	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}

	arg := &registry.Value{
		Name: v.Name(),
		Type: v.Name(),
	}

	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			val := extractValue(f.Type, d+1)
			if val == nil {
				continue
			}

			// if we can find a json tag use it
			if tags := f.Tag.Get("json"); len(tags) > 0 {
				parts := strings.Split(tags, ",")
				val.Name = parts[0]
			}

			// if there's no name default it
			if len(val.Name) == 0 {
				val.Name = v.Field(i).Name
			}

			arg.Values = append(arg.Values, val)
		}
	case reflect.Slice:
		p := v.Elem()
		if p.Kind() == reflect.Ptr {
			p = p.Elem()
		}
		arg.Type = "[]" + p.Name()
		val := extractValue(v.Elem(), d+1)
		if val != nil {
			arg.Values = append(arg.Values, val)
		}
	}

	return arg
}

func extractSubValue(typ reflect.Type) *registry.Value {
	var reqType reflect.Type
	switch typ.NumIn() {
	case 1:
		reqType = typ.In(0)
	case 2:
		reqType = typ.In(1)
	case 3:
		reqType = typ.In(2)
	default:
		return nil
	}
	return extractValue(reqType, 0)
}

func newSubscriber(topic string, sub any, opts ...SubscriberOption) ISubscriber {
	cfg := SubscriberConfig{
		AutoAck: true,
	}

	for _, o := range opts {
		o(&cfg)
	}

	handlers, endpoints := parseSubscriber(topic, sub, opts...)
	return &subscriber{
		rcvr:       reflect.ValueOf(sub),
		typ:        reflect.TypeOf(sub),
		topic:      topic,
		subscriber: sub,
		handlers:   handlers,
		endpoints:  endpoints,
		config:     cfg,
	}
}

func parseSubscriber(topic string, sub any, opts ...SubscriberOption) (handlers []*handler, endpoints []*registry.Endpoint) {
	var hd *handler
	switch v := sub.(type) {
	case func(*TSubscriberContext):
		hd = &handler{
			Id:     int(crc32.ChecksumIEEE([]byte(topic))),
			Config: &ControllerConfig{},
			Type:   SubscriberHandler,
			//pos:     -1,

			ctrlType:  reflect.TypeOf(v),
			ctrlValue: reflect.ValueOf(v),
		}
		hd.pos.Store(-1)
		hd.TransportType = SubscribeHandler
		hd.funcs = append(hd.funcs, &handle{IsFunc: true, SubFunc: v})

		handlers = append(handlers, hd)
		endpoints = append(endpoints, &registry.Endpoint{
			Name:    "Func",
			Request: extractSubValue(hd.ctrlType),
			Metadata: map[string]string{
				"topic":      topic,
				"subscriber": "true",
			},
		})

		return
	default:
		// init Value and Type
		ctrlValue, ok := sub.(reflect.Value)
		if !ok {
			ctrlValue = reflect.ValueOf(sub)
		}
		ctrlType := ctrlValue.Type()
		kind := ctrlType.Kind()
		switch kind {
		case reflect.Struct, reflect.Ptr:
			// transfer prt to struct
			if kind == reflect.Ptr {
				ctrlValue = ctrlValue.Elem()
				ctrlType = ctrlType.Elem()
			}
			// 获取控制器名称
			//
			// ★ 取名字要用 `sub`，不能用 `v`。`if v, ok := sub.(IString)` 把外层
			// type switch 的 v **遮蔽**掉了，else 分支里的 v 是那次**断言失败**的
			// 结果 —— 一个 nil 的 IString。`Obj2Name` 拿它去 reflect.Value.Type
			// 直接 panic："reflect: call of reflect.Value.Type on zero Value"。
			// 也就是说任何不实现 IString 的结构体订阅者，一登记就崩 —— 又一个
			// "这条路径从来没被走过"的证据。
			var objName string
			if v, ok := sub.(IString); ok {
				objName = v.String()
			} else {
				objName = utils.DotCasedName(utils.Obj2Name(sub))
			}

			var name string
			var method reflect.Value
			for i := 0; i < ctrlType.NumMethod(); i++ {
				// get the method information from the ctrl Type
				name = ctrlType.Method(i).Name

				// ★ 必须取**绑定过接收者**的方法值。
				//
				// 此前这里用的是 `ctrlType.Method(i).Func` —— 那是未绑定的形式，
				// 第一个入参是接收者本身，签名成了 func(T, *TSubscriberContext)，
				// 既不等于 SubFunc 的类型，调用时也没有接收者可传。于是下面那层
				// parseSubscriber 建出一个 funcs 为空的 handler，整个订阅者静默失效。
				// `Value.Method(i)` 给的才是 func(*TSubscriberContext)。
				method = ctrlValue.Method(i)

				// 忽略签名对不上的方法（结构体上通常还有别的方法）。
				if !method.CanInterface() || !isSubscriberFunc(method.Type()) {
					continue
				}

				hds, eps := parseSubscriber(objName+"."+name, method, opts...)
				handlers = append(handlers, hds...)
				endpoints = append(endpoints, eps...)
			}
			// the end of the struct mapping
			return
		case reflect.Func:
			// Method must be exported.
			if ctrlType.PkgPath() != "" {
				log.Fatalf("Method %s must be exported", v)
				return
			}

			// 签名对不上就不建 handler：建出来也永远不会被调用（InvokeSubscriber 只认
			// SubFunc），而"建了但不触发"正是这条路径以前的样子。返回空让
			// TGroup.Subscribe 的 "no handler functions" 直接报出来。
			if !isSubscriberFunc(ctrlType) {
				log.Errf("subscriber %q: 处理器签名是 %v，必须是 func(*router.TSubscriberContext)",
					topic, ctrlType)
				return
			}

			hd = &handler{
				Id:     int(crc32.ChecksumIEEE([]byte(topic))),
				Config: &ControllerConfig{},
				Type:   SubscriberHandler,
				//pos:     -1,

				ctrlType:  ctrlValue.Type(),
				ctrlValue: ctrlValue,
			}
			hd.Name = fmt.Sprintf("%s-%d", topic, hd.Id)
			hd.pos.Store(-1)
			hd.TransportType = SubscribeHandler
			// ★ 关键：把反射拿到的函数值装进 SubFunc。此前这一步整个缺失，
			// funcs 为空 → InvokeSubscriber 一个都不调 → 订阅者静默失效。
			fn := ctrlValue.Interface().(func(*TSubscriberContext))
			hd.funcs = append(hd.funcs, &handle{IsFunc: true, SubFunc: fn})
			handlers = append(handlers, hd)
			endpoints = append(endpoints, &registry.Endpoint{
				Name:    hd.Name,
				Request: extractSubValue(hd.ctrlType),
				Metadata: map[string]string{
					"topic":      topic,
					"subscriber": "true",
				},
			})
		default:
			log.Fatal("controller must be func or bound method")
		}
	}

	return handlers, endpoints
}

func (s *subscriber) Topic() string {
	return s.topic
}

func (s *subscriber) Subscriber() interface{} {
	return s.subscriber
}

func (s *subscriber) Endpoints() []*registry.Endpoint {
	return s.endpoints
}
func (s *subscriber) Handlers() []*handler {
	return s.handlers
}

func (s *subscriber) Config() SubscriberConfig {
	return s.config
}
