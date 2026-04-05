// Package goroutine provides safe goroutine helpers that catch panics so they
// cannot crash the host process.
package goroutine

import (
	"fmt"
	"runtime/debug"
)

// ErrHandler is called when a goroutine started with Go or GoWithContext panics.
// Receives the formatted panic message + stack trace.
type ErrHandler func(err error)

// Go starts fn in a new goroutine.  If fn panics the panic is recovered and
// onErr (if provided) is called with the error; the process is NOT terminated.
func Go(fn func(), onErr ...ErrHandler) {
	go safeRun(fn, mergeHandlers(onErr))
}

func safeRun(fn func(), onErr ErrHandler) {
	defer func() {
		if p := recover(); p != nil {
			err := fmt.Errorf("goroutine panic: %v\n%s", p, debug.Stack())
			if onErr != nil {
				onErr(err)
			}
		}
	}()
	fn()
}

func mergeHandlers(handlers []ErrHandler) ErrHandler {
	if len(handlers) == 0 {
		return nil
	}
	return handlers[0]
}
