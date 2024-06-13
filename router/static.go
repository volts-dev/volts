package router

import (
	"net/http"
	"path/filepath"
	"strings"

	"github.com/volts-dev/utils"
)

func staticHandler(urlPattern string, filePath string) func(c *THttpContext) {
	fs := http.Dir(filePath)                                        // the filesystem path
	fileServer := http.StripPrefix(urlPattern, http.FileServer(fs)) // binding a url to file server

	return func(ctx *THttpContext) {
		defer ctx.Apply() //已经服务当前文件并结束

		file := filepath.Join(ctx.pathParams.FieldByName("filepath").AsString())
		// Check if file exists and/or if we have permission to access it
		if _, err := fs.Open(file); err != nil {
			// 对最后一个控制器返回404
			if ctx.handlerIndex == len(ctx.route.handlers)-1 {
				ctx.response.WriteHeader(http.StatusNotFound)
			}

			log.Warn(err)
			return
		}

		fileServer.ServeHTTP(ctx.response, ctx.request.Request)
	}
}

// 支持服务器root文件夹下的文件
func rootStaticHandler(ctx *THttpContext) {
	defer ctx.Apply() //已经服务当前文件并结束

	p := ctx.PathParams()
	//fileName := strings.ToLower(p.FieldByName("fileName").AsString())
	fileExt := strings.ToLower(p.FieldByName("ext").AsString())
	if utils.In(fileExt, "", "html", "txt", "xml") == -1 {
		ctx.NotFound()
	}

	ctx.ServeFile(ctx.request.URL.Path[1:])
}
