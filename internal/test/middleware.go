package test

import "github.com/volts-dev/volts/router"

type (
	TestSession struct {
		Id string
	}
)

func NewTestSession(router router.IRouter) router.IMiddleware {
	return &TestSession{}

}

func (self *TestSession) Name() string {
	return "session"
}

func (self *TestSession) Handler(ctx router.IContext) {
	ctx.Write([]byte("In TestSession"))
	ctx.Next()
}
