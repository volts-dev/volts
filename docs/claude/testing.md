# 测试注意事项

## Testing Notes

- Tests that wait on an external event **must** carry a timeout. Three separate packages
  (logger, mdns×2, internal/test/client) each used to hang for the full test timeout
  (~300s), which drowned every real result. A hang is worse than a failure: it reports
  nothing.
- Do not bind fixed ports in tests. `server.Debug()` pins 35999/45999, which collides with
  a locally running kylin.
- **TCP reachability probes do not work on this machine.** macOS network extensions accept
  every TCP connect — `net.DialTimeout` to `192.0.2.1` (RFC 5737, unroutable) succeeds in
  0.01s. Tests hitting public endpoints must classify the *request* error instead
  (see `client/http_client_test.go:skipIfNetworkError`).
- **Measuring a proxy on this machine measures Clash, not the proxy.** A local Clash/mihomo
  in TUN mode intercepts every outbound TCP — *including a dial to a literal IP* — and
  re-routes it by its own rules, so "these nodes fail" may mean "mihomo tunnelled our
  handshake through a second hop". The tell is a local address in `198.18.0.0/15`; the proof
  is mihomo's own connection table
  (`curl --unix-socket /tmp/verge/verge-mihomo.sock http://localhost/connections`), which
  names the chain it picked. `transport/ss_live_test.go` (`VOLTS_SS_LIVE_NODES=<file>`) is
  the real judgment: it prints the **exit IP** per node, so a working proxy is visible as a
  different IP rather than as a 200 that direct traffic would also produce.
- **`httptest.ResponseRecorder` does not enforce net/http's body rules, and the real
  server does** — so a 304 or 204 carrying a body is visible in a unit test and invisible
  through `httptest.NewServer` (measured: 18 bytes written, 0 bytes on the wire; net/http
  answers `ErrBodyNotAllowed`). The two instruments disagree, which is worse than either
  being wrong: trust the recorder and you chase a defect that does not exist in production;
  trust the live server and you miss the day a different writer (the compression wrapper, a
  hijacked connection) actually lets it out. `THttpResponse.Write` therefore applies the
  rule itself. Regression: `router/http_bodyless_test.go` (recorder) — deliberately not a
  live test, since a live test passes either way.
- `transport/message_fuzz_test.go` holds the fuzz targets; deep runs with
  `gotip test -run=XXX -fuzz=FuzzMessageDecode -fuzztime=10m ./transport/`.
- **Measure before calling something an optimization.** `router/handler_bench_test.go`
  exists because a "hoist the type assertion out of the loop" change was committed as a
  perf win and turned out to be 15% *slower*. In short chains the surrounding ServeHTTP
  work (route match, context pool, ~19 allocs, ~2µs) drowns out anything the dispatch loop
  does — use the 50-middleware case, `-count=5`, and compare medians.
