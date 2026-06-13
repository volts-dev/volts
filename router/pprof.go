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

	group.Url([]string{"GET"}, "//", pprof.Index) //  "//" will tansfer to "/" like "/debug/pprof/"
	group.Url([]string{"GET"}, "/cmdline", pprof.Cmdline)
	group.Url([]string{"GET"}, "/profile", pprof.Profile)
	group.Url([]string{"GET"}, "/symbol", pprof.Symbol)
	group.Url([]string{"GET"}, "/trace", pprof.Trace)
	group.Url([]string{"GET"}, "/allocs", pprof.Handler("allocs"))
	group.Url([]string{"GET"}, "/block", pprof.Handler("block"))
	group.Url([]string{"GET"}, "/goroutine", pprof.Handler("goroutine"))
	group.Url([]string{"GET"}, "/heap", pprof.Handler("heap"))
	group.Url([]string{"GET"}, "/mutex", pprof.Handler("mutex"))
	group.Url([]string{"GET"}, "/threadcreate", pprof.Handler("threadcreate"))
	group.Url([]string{"POST"}, "/symbol", pprof.Symbol)

	return group
}
