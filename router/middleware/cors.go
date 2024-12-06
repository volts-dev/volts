package middleware

import (
	"net/http"

	"github.com/volts-dev/volts/router"
)

// Cors 为处理CORS（跨源资源共享）请求提供一个中间件函数。
// 它通过设置响应头，使服务器支持跨域请求。
func Cors() func(router.IContext) {
	// 返回的函数是一个中间件，它处理入站请求并设置CORS响应头。
	return func(ctx router.IContext) {
		// 将上下文断言为router.THttpContext类型，以便访问HTTP特定的方法和属性。
		if c, ok := ctx.(*router.THttpContext); ok {
			// 获取请求的方法。
			method := c.Request().Method

			// 设置允许跨域请求的响应头。
			c.Header().Set("Access-Control-Allow-Origin", "*")
			c.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE, UPDATE")
			c.Header().Set("Access-Control-Allow-Headers", "*")
			c.Header().Set("Access-Control-Expose-Headers", "Content-Length, Access-Control-Allow-Origin, Access-Control-Allow-Headers, Cache-Control, Content-Language, Content-Type")
			c.Header().Set("Access-Control-Allow-Credentials", "true")

			// 如果是OPTIONS请求，则直接终止请求，并返回204 No Content状态码。
			if method == "OPTIONS" {
				c.Abort("", http.StatusNoContent)
			}
			// 对于其他请求方法，继续执行下一个中间件或处理函数。
			c.Next()
		}
	}
}
