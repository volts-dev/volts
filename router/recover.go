package router

import (
	"bytes"
	"fmt"
	"runtime"
)

// report error information
func recoverHandler(ctx IContext) {
	if err := recover(); err != nil {
		//是否绕过错误处理直接关闭程序
		if ctx.Router().Config().RecoverHandler == nil {
			panic(err)
		}

		// 废弃 handle middleware
		//if handler.IsValid() {
		//	self.routeMiddleware("panic", route, ctx, handler)
		//}
		buf := bytes.NewBufferString("")
		buf.WriteString("recover:" + ctx.Route().Path + "\n")
		for i := 1; ; i++ {
			_, file, line, ok := runtime.Caller(i)
			if !ok {
				break
			}
			buf.WriteString(fmt.Sprintf("  %s %d\n", file, line))
		}
		log.Err(buf.String())
	}

}
