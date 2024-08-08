package router

import (
	"net/http/pprof"
)

const (
	// DefaultPrefix url prefix of pprof
	DefaultPrefix = "/debug/pprof"
)

func PprofGroup() *TGroup {
	group := NewGroup(
		WithGroupPathPrefix(DefaultPrefix),
	)

	group.Url("GET", "//", pprof.Index) //  "//" will tansfer to "/" like "/debug/pprof/"
	group.Url("GET", "/cmdline", pprof.Cmdline)
	group.Url("GET", "/profile", pprof.Profile)
	group.Url("GET", "/symbol", pprof.Symbol)
	group.Url("GET", "/trace", pprof.Trace)
	group.Url("GET", "/allocs", pprof.Handler("allocs"))
	group.Url("GET", "/block", pprof.Handler("block"))
	group.Url("GET", "/goroutine", pprof.Handler("goroutine"))
	group.Url("GET", "/heap", pprof.Handler("heap"))
	group.Url("GET", "/mutex", pprof.Handler("mutex"))
	group.Url("GET", "/threadcreate", pprof.Handler("threadcreate"))
	group.Url("POST", "/symbol", pprof.Symbol)

	return group
}
