# 设了也没人读的选项

## Options that set a field nobody reads

A recurring shape in this repo, and the reason two batch scans exist: a `WithX()` writes a
struct field, the field has **zero readers**, and the caller gets no diagnostic. Six were
removed at once — `router.WithIgnoreCase`, `router.WithStaticHandler`,
`client.AllowRedirect`, `client.WithStreamTimeout`, `logger.WithWrite`, and the whole
`internal/acme/options.go` (its `Provider` interface takes no options; the autocert
implementation says so in its own package comment).

Two of them are worth knowing individually:

- **`WithIgnoreCase` was applied by default and then ignored.** `NewGroup` built every
  group's tree with it, so the framework declared case-insensitive routing and never
  implemented it. It was deleted rather than implemented: turning it on would change the
  match set of every route already in production, `/{module}/static/...` resolves onto a
  case-sensitive filesystem (so "route matched, file missing" becomes possible), and
  case-insensitive path matching is a classic ACL-bypass shape — the middleware checks one
  casing, the backend opens another. `router/case_sensitive_routing_test.go` pins the
  property.
- **`server.AutoCreate` was the one that needed both sides.** It had no reader either, but
  vectors' `service.go` called it twice, so it stayed behind one release marked
  `Deprecated` and came out once those two call sites were gone.

Deleting `internal/acme/options.go` had a second effect worth noting: it was the **only**
file importing `github.com/go-acme/lego/v4`, so the whole ACME certificate library left
the dependency tree. A dead options struct nobody could reach was compiling a TLS/ACME
library into every binary that linked volts.

Separately, `HttpTransport.Listen`'s ACME branch wrote `config, err := ...`, which shadowed
the outer `err`; the later `l, err = vnet.Listen(...)` then assigned to the **inner** one,
so the function's own error check could never fire. An ACME listen failure returned a
listener wrapping `nil` and the server reported a successful start while listening on
nothing. `go vet` does not catch this — the `shadow` analyzer
(`go vet -vettool=$(go env GOPATH)/bin/shadow ./...`) does, and the rest of what it reports
here is benign inner scoping.
