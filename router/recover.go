package router

import (
	"bytes"
	"fmt"
	"runtime"
)

// report error information
func recoverHandler(ctx IContext) {
	buf := bytes.NewBufferString("")
	buf.WriteString("recover:  path:" + ctx.Route().Path() + "\n")
	for i := 1; ; i++ {
		_, file, line, ok := runtime.Caller(i)
		if !ok {
			break
		}
		buf.WriteString(fmt.Sprintf("  %s %d\n", file, line))
	}
	log.Err(buf.String())

	// 尽力向客户端回写错误响应：若只记日志不回写，连接会被无响应关闭——RPC 客户端
	// 表现为 EOF、HTTP 客户端表现为空回复，调用方无从得知失败原因。编码器走可插拔的
	// TypedErrorEncoder，与 typed handler 的错误信封协议保持一致（上层可覆盖）。
	defer func() { _ = recover() }() // 回写自身出错不再二次扩散
	TypedErrorEncoder(ctx, fmt.Errorf("internal error: handler panic recovered (see server log)"))
}
