package http

import (
	"fmt"

	"github.com/volts-dev/volts/broker"
	"github.com/volts-dev/volts/router"
)

func (h *httpBroker) Handler(ctx *router.THttpContext) {
	var m broker.Message
	err := ctx.Body().Decode(&m)
	if err != nil {

	}

	topic := m.Header["v-topic"]
	delete(m.Header, "v-topic")

	if len(topic) == 0 {
		//errr := merr.InternalServerError("go.micro.broker", "Topic not found")
		err := fmt.Errorf("Error parsing request body: %v", err)

		ctx.WriteHeader(500)
		ctx.Write([]byte(err.Error()))
		return
	}

	event := &event{
		m: &m,
		t: topic,
	}
	id := ctx.MethodParams().FieldByName("id").AsString()

	//nolint:prealloc
	var subs []broker.Handler

	ctx.Router()
	h.RLock()
	for _, subscriber := range h.subscribers[topic] {
		if id != subscriber.id {
			continue
		}
		subs = append(subs, subscriber.fn)
	}
	h.RUnlock()

	// execute the handler
	for _, fn := range subs {
		event.err = fn(event)
	}
}
