package http

import (
	"github.com/volts-dev/volts/broker"
)

type (
	event struct {
		m   *broker.Message
		t   string
		err error
	}
)

func (h *event) Ack() error {
	return nil
}

func (h *event) Error() error {
	return h.err
}

func (h *event) Message() *broker.Message {
	return h.m
}

func (h *event) Topic() string {
	return h.t
}
