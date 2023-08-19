package router

import (
	"net/http"
	"path/filepath"
)

func staticHandler(urlPattern string, filePath string) func(c *THttpContext) {
	fs := http.Dir(filePath)                                        // the filesystem path
	fileServer := http.StripPrefix(urlPattern, http.FileServer(fs)) // binding a url to file server

	return func(c *THttpContext) {
		file := filepath.Join(c.pathParams.FieldByName("filepath").AsString())
		// Check if file exists and/or if we have permission to access it
		if _, err := fs.Open(file); err != nil {
			// 对最后一个控制器返回404
			if c.handlerIndex == len(c.route.handlers)-1 {
				c.response.WriteHeader(http.StatusNotFound)
			}

			log.Warn(err)
			return
		}

		fileServer.ServeHTTP(c.response, c.request.Request)
		c.Apply() //已经服务当前文件并结束
	}
}
