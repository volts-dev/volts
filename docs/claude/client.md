# 客户端：HTTPS 拨号、响应编解码、重试

## HttpClient could not do HTTPS at all

`client.HttpClient`'s HTTPS path ended in a **panic on the caller's goroutine**, not an
error. `roundTripper.dialTLS` only registered a transport when the connection satisfied
`transport.INegotiatedProtocol` — which `core/ja3`'s utls wrapper does and **`*tls.Conn`
does not** (crypto/tls exposes ALPN through `ConnectionState()`, not a method of that
name). With no fingerprint configured nothing was ever written to `cachedTransports`, and
`RoundTrip` then called a method on a nil interface. vectors only uses this client for
plain intra-cluster HTTP, so it never fired there — but turning on TLS between services
would have taken down every caller at once.

Three things had to move together, and the second only becomes reachable once the first
is fixed:

- **Read ALPN by capability, not by one vendor's type.** `transport.NegotiatedProtocolOf`
  accepts both shapes. On top of that `dialTLS` now falls back to HTTP/1.1 when the
  protocol cannot be read at all — an unknown ALPN must land on a working default, never
  on "register nothing". `RoundTrip` returns a readable error if the map is still empty,
  because a library must not panic in its caller's goroutine.
- **A configured proxy was silently bypassed.** `DialConfig.ProxyURL` had **no reader
  anywhere**, and `dialCfg.dialer` was consulted only inside `HttpTransport.Dial`'s JA3
  branch — plain TLS went out through `tls.DialWithDialer`, i.e. direct. So "proxy
  configured, no fingerprint" put the request on the host's own IP while the caller
  believed otherwise, which is the exact failure `core/outbound`'s `allow_direct=false`
  exists to prevent. Every branch now dials through one `rawDial`, which prefers
  `WithDialer`, then builds one from `WithProxyURL`, and otherwise keeps the old
  env-proxy-aware `newConn` wrapper. `tls.Client` needs `ServerName` filled in by hand
  (`tls.Dial` infers it; `tls.Client` does not) and a handshake deadline, or a peer that
  completes TCP and never sends ServerHello holds the caller forever.
- **`User-Agent` was wiped on every request.** `call()` computes it from
  `config.UserAgent` / `Ja3.UserAgent`; `RoundTrip` then did an unconditional
  `Set("User-Agent", rt.UserAgent)` against a field the constructor never filled. Every
  request went out with an empty UA and the whole `WithUserAgent` / JA3-UA pairing was
  dead. It is now only applied when non-empty.

Two smaller ones in the same path: `RoundTrip` did all its map work — nil check, make,
read, write — outside the lock that `dialTLS` holds, so concurrent requests raced (and
`concurrent map writes` is a crash, not a warning); and the per-dial option slice was
`append`ed to **in place** inside the captured closure, growing by three entries per dial
without bound and racing on the same slice variable. `cfg.DialOptions` — written by
`Init`, read by nobody — is now the single source both the constructor and `Init` seed, so
`Init(WithJa3(...))` / `Init(WithProxyURL(...))` finally take effect instead of silently
doing nothing.

Regression: `client/https_dial_test.go`. `TestProxy_IsUsedForHTTPSWithoutFingerprint`
stands up a real CONNECT proxy and counts hits — asserting on the dial helper instead
passes even when the plain-TLS branch dials direct, because the raw dial has already gone
through the proxy by then.

## A client response decodes with the *response's* codec

`httpResponse` used to build its body with the **request's** codec
(`body.New(req.body.Codec)`). For a POST that sends JSON and receives JSON that happens to be
right; for a GET it is not — a request with no body gets the bytes codec, so
`rsp.Body().Decode(&out)` fails with `... is not a []byte`, which reads as "the peer returned
the wrong format" rather than "we picked the wrong decoder". vectors' `core/codec` takes
exactly this path. The codec now comes from the response's `Content-Type`, falling back to the
request's when it is absent or unrecognised.

**Use `codec.IdentifyWireCodec`, never `IdentifyCodec`, for that lookup.** `Content-Type` is
filled in by the peer, so it decides which decoder we point at the peer's own bytes — the same
rule as the RPC header byte, and the reason Gob is not wire-selectable.
`TestResponseCodec_PicksByResponseContentType` pins it, including that a response claiming
`gob` gets the fallback.

While there: `httpResponse.Read(out)` was a stub whose whole body was `return nil` — the
caller got an untouched zero value and a nil error. It is not in `IResponse` and had no
callers, but `HttpResponse` is an exported alias, so it was reachable. It now decodes.
`Body()` also dropped a dead re-read of `self.response.Body`, which by then is already closed
and drained; its error check was inverted (`if err == nil { log.Err(err) }`), logging a nil on
success and staying silent on real failures.

## Retries are off by default, and that is deliberate

`client.DefaultRetry` is `RetryNever`. Retrying a non-idempotent call means executing it
twice, and this stack carries sale / purchase / account / payment — the same reasoning as the
reverse proxy's refusal to replay POSTs. Opting in takes two knobs, both of which are now
real: `client.WithRetries(n)` (the attempt count — `Config.Retries`, i.e. `client.*.retries`
in config.json, had **no reader at all** and was a knob that did nothing) and
`client.WithRetry(fn)` (the policy).

- **`RetryOnConnectError` is the only provably safe policy.** It fires solely on
  `client.ErrConnectFailed` — the connection was never established, so not one byte of the
  request was sent and the server cannot have executed it. A bare `EOF` after the request went
  out is deliberately excluded: "the server never got it" and "the server finished and then
  the connection dropped" are indistinguishable.
- **`RetryOnError` (408/500) is for idempotent reads only.** A 500 means the server *did*
  execute it. It also used to be dead code: it read the status via `errors.Parse(err.Error())`,
  and `Error()` returns only `Detail`, so `Parse` always failed to decode and left `Code` at 0
  — the switch never matched and it returned false for every error, silently.
- The RPC client never consults the policy when the server answered with a non-OK status bit;
  it returns immediately. So on the RPC side the connect-only policy covers every case that is
  retryable at all.
