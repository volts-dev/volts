# 连接限制：四段超时，而 Debug() 只许放宽其中两段

## Connection limits: four segments, and `Debug()` may only widen two

`transport.applyServerLimits` is the single place that configures every `http.Server` this
package builds (there are two construction sites, and they used to disagree —
`transport/listener.go` set no timeout and no header cap at all). Four settings each bound a
different segment of a connection's life, and a segment left unbounded is unbounded:

| segment | setting |
|---|---|
| handshake → request headers read | `ReadHeaderTimeout` (20s) |
| headers → request body read | `ReadTimeout` (15s) |
| first write → response written | `WriteTimeout` (15s) |
| between keep-alive requests | `IdleTimeout` (120s) |

**`ReadHeaderTimeout` and `IdleTimeout` fall back to `ReadTimeout` when unset**, so every
deployment that raises `ReadTimeout` for a legitimate reason silently opens those two as
well. `transport.Debug()` raises it to 999s — and vectors' `cmd.Start()` calls
`volts.Debug()` **unconditionally** for both servers (measured: read/write/dial all become
16m39s). That left every process in production willing to spend sixteen minutes receiving a
few hundred bytes of request header, which is precisely the Slowloris precondition. So these
two are configured independently and `Debug()` does not touch them: a debugger breakpoint
sits inside a handler, which is *after* the headers are read.

Regression: `transport/server_limits_test.go`. `TestSlowHeaderConnectionIsClosed` is
behavioural, not a field assertion — it dribbles half a request line into a real socket and
asserts the server gives up on schedule (drop the setting and it hangs for the full
`ReadTimeout`). Note the server answers a header timeout with `400` + `Connection: close`
before closing, so the judgement is *when* it gives up, not what it says.

# 请求头上限：最小的那堵墙算数，而它拒起来一声不吭

## The header cap: the smallest wall wins, and it rejects in silence

`MaxHeaderBytes` is the fifth setting `applyServerLimits` owns, and it is the only one whose
violation is answered *before any handler exists*: the standard library reads the request
line and headers under a limit of `MaxHeaderBytes + 4096`, and on overflow writes a bare
`431 Request Header Fields Too Large` and closes the connection. No route, no middleware,
no log line — on either side.

`DefaultMaxHeaderBytes` was 8KiB, justified as "enough for the usual cookies + UA + custom
headers". That justification was falsified on a real machine, and the flaw in it is
structural: **the volume of cookies is not this site's decision.** Browsers send cookies by
*domain*, ignoring the port and ignoring who is listening. A hostname under a shared parent
domain therefore receives every cookie that parent ever set — this repo's workbench exit
deliberately lives under `.ti.com` so that connect_components' proxy looks like TI's domain,
so accumulating them is inevitable, not accidental. Measured on the affected browser: 12–16KB.

**Only the smallest wall in the chain counts, and a relayed 431 does not say which one it
was.** The deployment is three hops, each with its own wall and its own `config.json`:

| hop | wall | where |
|---|---|---|
| vite dev server (Node) | `--max-http-header-size`, 16KiB default | `vertex/package.json` dev script |
| gateway (kylin) | `transport.default.max_header_bytes` | `kylin/config.json` |
| business process (websvc / salesvc / …) | same key, its **own** file | `kylin/cmd/*/config.json` |

Two separate mornings were spent bisecting this by hand, because raising one hop just moves
the symptom: with the gateway at 32KiB and websvc still at 8KiB, the *page* renders (vite
serves it) while everything proxied to websvc dies — which reads as "the site is broken" or
"the password is wrong", never as "a header cap". The tell is in the 431 itself:

- `Connection: close`, no `Date` → the standard library rejected it **on this hop**.
- `Date` + `Transfer-Encoding: chunked` → a **handler wrote it**, i.e. a proxy relayed an
  upstream's rejection. The hop that answered you is not the hop that refused you.

Two conclusions, both now enforced:

1. **The framework must supply a number that works; config only raises it further.** Every
   process carries its own `config.json` (they must — three processes sharing one file get
   it rewritten into half-truncated JSON at startup), so per-file configuration means every
   new service is the same landmine re-armed. `DefaultMaxHeaderBytes` is 32KiB: one notch
   above Node's 16KiB so the dev server does not become the new bottleneck, still 32× under
   the standard library's 1MB. Regression:
   `TestDefaultHeaderLimit_AcceptsBrowserCookieLoad` — a real socket (a recorder cannot
   reach this limit at all) carrying a 16KiB cookie must be answered `200`, and 1MiB must
   still be refused so the test is not a licence to keep widening.
2. **A relayed 431 must name the hop.** `router.HttpReverseProxy` logs the target and the
   inbound header size whenever an upstream answers 431 — the two numbers that the manual
   bisection existed to recover. Regression:
   `TestHttpReverseProxy_NamesTheHopThatRejectedOversizedHeaders`, whose upstream is a real
   server with a deliberately tiny cap (a handler calling `WriteHeader(431)` would not
   exercise "the library refused before the handler ran").

## In-process dialing: a pipe for calls to yourself

A vectors process hosts dozens of module services on one RPC listener, and they call each
other through the registry like strangers — select a node, dial `127.0.0.1:<own port>`,
round-trip through the kernel (and through whatever network extension this Mac has
installed). `transport/inproc.go` cuts exactly that segment: when `tcpTransport.Dial`'s
target is a TCP listener **in this process**, it returns one end of a `net.Pipe` and hands
the other to that listener's own `serveConn` loop.

The cut is at `Dial` on purpose. Everything except the `net.Conn` is the same code — pool,
frame codec, header metadata, deadlines, the server's `context.Background()`-rooted request
context (the caller's context values never cross), panic recovery, routing and the whole
middleware chain. A shortcut higher up (calling the handler function directly) would save
more and would also create one more place where "same process" and "other process" behave
differently, which is the thing the framework is trying to eliminate.

Rules:

- Registered by port in `tcpTransportListener.Serve`, removed when `Serve` returns. "Local
  address + port in the table" identifies this process because a host port has one owner.
- **Not used with a transport's own TLS** (`Secure` / `TlsConfig`) on either side — the pipe
  carries no certificate. Process-wide peer TLS (below) is the exception: it proves *which
  process* is calling, and both ends of a pipe are this process.
- `transport.DisableInproc()` (config `disable_inproc`) forces the real network. **Any test
  about the TCP wire itself must set it**: tests start their server in the same process, so
  without it they silently test the pipe. `transport/tcp_test.go` does.
- Server idle `ReadTimeout` applies to pipes too (`net.Pipe` honours deadlines); the pool's
  liveness probe sees EOF on a pipe the server closed, same as a TCP FIN.

How to tell which path a request took: `req.Context.Value(transport.RemoteConnContextKey)`
is a `net.Conn` whose `RemoteAddr().Network()` is `"pipe"`. Regressions:
`transport/inproc_test.go`, `internal/test/client/rpc_inproc_test.go`
(`TestRpcClient_LocalServerGoesInprocWithSameResult` — full client→router path, pipe vs TCP
must differ only in connection kind).

## Peer TLS: process-wide mTLS for service-to-service RPC

`transport.SetPeerTLS(&PeerTLS{Mode, Server, Client})` (`transport/peer_tls.go`) applies to
**every TCP transport in the process that has no `Secure` / `TlsConfig` of its own**. It is
process-wide because RPC clients are created all over an application (one per service name,
plus controllers calling `NewRpcClient` themselves); configuring each one would miss one, and
under `On` the one you missed just fails to connect.

| Mode | listener accepts plaintext | listener accepts TLS | dials with |
|---|---|---|---|
| `Off` | yes | — | plaintext |
| `Accept` | yes | yes | plaintext |
| `Dual` | yes | yes | TLS |
| `On` | **no** (closed, logged) | yes | TLS |

Roll forward one step at a time across **all** processes (Accept → Dual → On); within each
step processes in adjacent modes interoperate, so restarts can go one by one.

- One port serves both, told apart by the **first byte**: a TLS record starts `0x16`, an RPC
  frame starts `MagicNumber` (`0x08`). Sniff + handshake run in the connection's own goroutine
  with a deadline (min of `ReadTimeout`, 10s) — a silent peer cannot stall `Accept`.
- **A listener's mode is fixed when `Listen` is called**, like `Secure`. Call `SetPeerTLS`
  before the server starts.
- Identity is the caller's business: `Server` must carry `ClientAuth` + verification,
  `Client` carries the client certificate. Without a CA, use `InsecureSkipVerify` **plus**
  `VerifyPeerCertificate` that pins the public key — never `InsecureSkipVerify` alone.
- In-process dialing still wins over TLS. `transport.PeerOf(conn)` tells a handler what is on
  the other end: TLS peer certificates, `inproc=true` (this process), or neither (plaintext).
- `transport.LoopbackOnly()` (config `loopback_only`) binds `127.0.0.1:PORT` — the only way
  to do so, because the default listen widens `127.0.0.1:PORT` to `:PORT`
  (`normalizeBindAddr`). The registered address becomes `127.0.0.1`, so only same-host peers
  can reach it.

Regressions: `transport/peer_tls_test.go`.
