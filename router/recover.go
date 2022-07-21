package router

import (
	"bytes"
	"fmt"
	"runtime"
)

// report error information
func recoverHandler(ctx IContext) {
	buf := bytes.NewBufferString("")
	buf.WriteString("recover:  path:" + ctx.Route().Path + "\n")
	for i := 1; ; i++ {
		_, file, line, ok := runtime.Caller(i)
		if !ok {
			break
		}
		buf.WriteString(fmt.Sprintf("  %s %d\n", file, line))
	}
	log.Err(buf.String())
}
