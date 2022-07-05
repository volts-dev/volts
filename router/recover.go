package router

import "runtime"

func Recover(ctx IContext) {

	// report error information
	for i := 1; ; i++ {
		_, file, line, ok := runtime.Caller(i)
		if !ok {
			break
		}
		log.Errf("file: %s %d", file, line)
	}
}
