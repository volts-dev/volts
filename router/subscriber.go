package router

import (
	"context"
	"fmt"
	"hash/crc32"
	"reflect"
	"strings"

	"github.com/volts-dev/utils"
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

func validateSubscriber(sub ISubscriber) error {
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
			Id:      int(crc32.ChecksumIEEE([]byte(topic))),
			Manager: deflautHandlerManager,
			Config:  &ControllerConfig{},
			Type:    SubscriberHandler,
			pos:     -1,

			ctrlType:  reflect.TypeOf(v),
			ctrlValue: reflect.ValueOf(v),
		}

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
			var objName string
			if v, ok := sub.(IString); ok {
				objName = v.String()
			} else {
				objName = utils.DotCasedName(utils.Obj2Name(v))
			}

			var name string
			var method reflect.Value
			for i := 0; i < ctrlType.NumMethod(); i++ {
				// get the method information from the ctrl Type
				name = ctrlType.Method(i).Name
				method = ctrlType.Method(i).Func

				// 忽略非handler方法
				if method.Type().NumIn() <= 1 {
					continue
				}

				if method.CanInterface() {
					hds, eps := parseSubscriber(objName+"."+name, method, opts...)

					handlers = append(handlers, hds...)
					endpoints = append(endpoints, eps...)
				}

			}
			// the end of the struct mapping
			return
		case reflect.Func:
			// Method must be exported.
			if ctrlType.PkgPath() != "" {
				log.Fatalf("Method %s must be exported", v)
				return
			}

			hd = &handler{
				Id:      int(crc32.ChecksumIEEE([]byte(topic))),
				Manager: deflautHandlerManager,
				Config:  &ControllerConfig{},
				Type:    SubscriberHandler,
				pos:     -1,

				ctrlType:  ctrlValue.Type(),
				ctrlValue: ctrlValue,
			}
			hd.Name = fmt.Sprintf("%s-%d", topic, hd.Id)

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
