# 出站：core/ja3 指纹与 core/outbound

## Optional Submodule: `core/ja3`

`core/ja3/` is a **separate Go module** (`github.com/volts-dev/volts/core/ja3`) holding the
TLS fingerprint-spoofing implementation. The main module deliberately does not depend on it,
and therefore does not depend on `github.com/refraction-networking/utls` — a library whose
purpose is mimicking other clients' TLS fingerprints, which most people writing business
services do not need but would otherwise compile into every binary and have flagged in
compliance scans.

Enable it with a blank import:

```go
import _ "github.com/volts-dev/volts/core/ja3"
```

**Prefer a preset name over a JA3 string.** `Dial(conn, host, cfg, "chrome_133")` uses
utls's byte-exact ClientHello; a JA3 string is only ever an approximation, because JA3
records extension **ids** and not their contents — and GREASE, Chrome's per-connection
extension shuffling, the post-quantum key share and ALPS all live in the contents.
Measured (`tls.peet.ws`): the preset yields JA4 `t13d1516h2_8daaf6152771_…` (Chrome's real
cipher hash); a hand-copied JA3 string yields `t13d3112h2_e8f1e7e78f70_…` — 31 ciphers where
Chrome sends 15, i.e. a client that is *nobody*, and therefore rarer than Go's own default.
The dispatch is by shape: a JA3 string always has exactly four commas; anything else is
looked up as a preset name, and an unknown name is an error that lists the valid ones.

**Rebuilding a ClientHello may only restore what the string contains — never invent.**
A GREASE key_share added to a string whose curve list has no GREASE violates RFC 8446
§4.2.8 (no key_share for a group absent from supported_groups). Go's own TLS server does
not check this, so httptest stays green; Akamai does, and answers `illegal parameter` —
an error that names neither extension. Guards must therefore be **structural assertions**
over the spec (`TestKeyShareIsSubsetOfSupportedGroups`), not a successful handshake.
`boundary_live_test.go` (`VOLTS_JA3_LIVE=1`) handshakes every preset against Akamai,
Cloudflare and Google — that is the only thing that answers "does this fingerprint work".

`transport` keeps two things: the hook (`transport/ja3_hook.go` — the `Ja3` config struct,
the dialer registry, `Ja3Dialer()` for callers building their own `DialTLSContext`, and
`INegotiatedProtocol` so callers can read ALPN without asserting a concrete TLS type), and
the **metadata registry** (`transport/fingerprint.go` — each fingerprint's name, browser
family and self-consistent User-Agents). Metadata needs no utls, so keeping it in the main
module lets upper layers enumerate identities, pair UA with fingerprint, and branch on
family — without writing utls into their own go.mod. Setting a fingerprint **without**
importing the submodule returns `transport.ErrJa3NotRegistered` rather than silently
falling back to ordinary TLS — a silent fallback would leave the caller believing they
were disguised.

The JA3 integration tests live in the submodule (`core/ja3/client_integration_test.go`);
they cannot stay in the main module, since importing the submodule from there would both
create a module cycle and drag utls back into the main dependency tree.

## Outbound: talking to strangers

`core/outbound/` is the HTTP client for scraping **third-party** sites. `client/` calls
*your own* services (discovery, balancing, breakers, RPC codecs); this one calls people who
do not know you and have no obligation to cooperate, so the question shifts from "how do I
find it" to "why would it answer me". Both share transport's dialing and fingerprint
registry; nothing above that is common, so they stay separate.

```go
cli := outbound.Client()   // a plain *http.Client from here on
```

An identity is **fingerprint + UA + client hints + exit IP**, pinned together at pool build
time, each with **its own connection pool** — sharing one `http.Transport` would let a
connection that handshook as mac Chrome carry a request whose UA says win Chrome, and two
identities on one TLS connection is more recognisable than no disguise at all. UA comes
from the fingerprint and `sec-ch-ua` is **derived from the UA**, so there is no opportunity
to mistype either; family decides whether client hints are sent at all (Chromium only —
Firefox sends `Sec-Fetch-*` but no hints, Safari sends neither).

**Being refused is not a transport error.** A 403 arrives over a connection that worked:
TCP connected, TLS handshook, the peer answered. To `http.RoundTripper` that is a *success*,
so without special handling a blocked identity keeps hammering the same endpoint with the
same fingerprint forever — which is precisely the case the disguise exists for. Statuses in
`RotateOnStatus` (default 403 + 429) therefore mean "this identity is blocked": cool it
down, **drop its idle connections** (WAF verdicts often bind to the connection/session, so
keeping it warm makes the cooldown meaningless), and retry on the next identity. When every
attempt is refused the **last response is handed back verbatim** — the caller needs to see
the 403 itself, not a rewritten error. 429's `Retry-After` outranks our own backoff (both
delta-seconds and HTTP-date), still capped at `cooldownMax`.

503 is deliberately **not** in the default list: it is both Cloudflare's challenge page and
"the site is actually down", and the response cannot tell them apart — treating it as a
block multiplies traffic to an already-struggling peer by `maxAttempts`.

**Proxies can be swapped at runtime, and they may be `ss://` / `ssr://`.**
`outbound.SetProxies(urls)` replaces the list and rebuilds the pool; `nil` drops the
override (back to config.json), an empty slice means "nothing configured" (= direct), and a
**failed build keeps the old pool** — refusing the change is better than swapping in an
empty pool that silently kills every exit. A caller must push the proxies the operator
*enabled*, never the ones that last *tested green*: filtering on health makes one network
blip empty the list, and "configured a proxy" quietly becomes "direct", which is exactly
what `allow_direct=false` exists to prevent.

`transport.NewProxyDialer` speaks `http`/`https`/`socks5` **plus `ss` and `ssr`**
(`transport/ss_proxy.go`), because the proxies people actually buy in the Chinese market
come as airport subscriptions whose nodes are ss/ssr — supporting only http/socks5 makes a
subscription import land 149 nodes of which **0** are usable. The line is drawn at ss
(AEAD + stream ciphers) and ssr with `protocol=origin`, `obfs=plain|http_simple` — that tier
is really just classic shadowsocks plus an HTTP-shaped header, whereas `auth_chain_*` is a
second stateful protocol machine and hysteria2/tuic need a whole QUIC stack. Everything else
**errors and names the protocol**; guessing "let us try it as origin" builds a connection
that then silently drops every packet server-side. Layer order is load-bearing —
`payload → [SS cipher] → [SSR obfs] → TCP`, obfs wrapping the *ciphertext* — and CFB is
**not symmetric** (encrypter one way, decrypter the other; sharing one still "connects",
then every byte is wrong). `TestSsRoundTrip` runs every cipher against an in-process fake
server for exactly these "connects fine, data is garbage" failures.

`RedactProxy` blanks ss/ssr URLs **entirely** (keeping only the `#name`): their credentials
are not in front of the `@`, so the old `@`-based redaction left passwords in the log.

**Concurrent requests on one line are a signal in themselves.** A line is an identity —
fingerprint + UA + exit IP. Two requests in flight on one line means either a second TCP
connection (h1) or a second stream (h2), i.e. one browser on one IP issuing several
unrelated requests in the same millisecond. So each identity carries a gate of capacity 1
(`MaxPerIdentity`): a request that cannot get a slot **waits** rather than going out on an
identity that is already busy. Parallelism is not cancelled, it is redirected onto other
lines — which is also the right way to add throughput: add an exit, don't widen the gate.
The slot is held until the **response body is closed**, not until headers arrive (the
connection is still transferring when headers come back, so releasing early lets the next
request overlap after all); `maxSlotHold` is the leak valve for a caller that forgets
`Body.Close()`, because turning a leaked connection into a permanently wedged line would
be a worse failure than the one being prevented. `outbound.Concurrency()` reports
lines × gate, so a caller's own worker pool can be sized to it — a pool wider than that
just queues, and that queueing is charged to the caller's own ctx, which turns "we
oversubscribed" into what looks like "the peer timed out".

Seven rules that are easy to get wrong:

- **One exit means one identity — but only in steady state.** Rotating six browser
  identities out of a single IP, each polling the same endpoints on the same schedule, is
  *more* machine-like than one stable identity — real people behind one exit do not behave
  identically. So the *active* identity count follows proxy count. The leftover fingerprints
  are not discarded: they become **reserves**, picked only when every active identity is
  cooling. Once you have been refused, "looking natural" has already failed, and the choice
  is between an identity that is certainly blocked and one that might not be. Naming
  `profiles` explicitly means "these exact ones" — all active, no reserves.
- **Reserves cover *blocked*, never merely *busy*.** Falling through to a reserve because
  the active is mid-request would put two identities on one exit simultaneously — which is
  the "same IP, several browsers" pattern this package argues against. A busy active is
  worth waiting for; a blocked one is not. Regression:
  `TestBusyActivesDoNotPromoteReserves`.
- **Rotation cannot change the IP.** With no proxy configured all identities share one exit,
  so an IP-based limiter is unaffected by any amount of fingerprint rotation. The first
  block logs this once (`warnIfDirect`) rather than letting someone watch a feature that is
  "already rotating" and guess why nothing improves.
- **Replay needs `GetBody`, not just an idempotent method.** A refusal means the peer never
  processed the request, so replaying is *semantically* safe — but `Body` is a one-shot
  reader, and retrying without rewinding silently sends an **empty** request whose symptom
  ("the peer says the parameters are wrong") points nowhere near the cause. `http.NewRequest`
  fills `GetBody` for bytes/strings bodies; anything else is not retried.
- **A configured proxy is never silently bypassed.** `allow_direct` defaults to false:
  when every proxy is unusable the request fails with `ErrNoUsableProxy` instead of going
  out on the host's own IP. People configure proxies precisely so the far end does not see
  that IP; a silent fallback quietly does the opposite, with no trace.
- **ALPN must be probed explicitly.** `http.Transport` reads ALPN by type-asserting
  `*tls.Conn`; a utls connection is not one, so the transport speaks HTTP/1.1 no matter what
  the peer selected — and an h2 peer receiving HTTP/1.1 bytes produces an error
  indistinguishable from "the site changed its response format".
- **HTTP/2 fingerprints cannot be matched, and half-matching is worse than not trying.**
  Go's SETTINGS are `2:0;4:4194304;5:1048576;6:10485760|1073741824|0|a,m,p,s`; Chrome 133 is
  `1:65536;2:0;4:6291456;6:262144|15663105|0|m,a,s,p`. Setting 5 is one Chrome never sends
  and Go always does, the order differs, and the pseudo-header order is hardcoded in
  `encodeHeaders`. Tuning the reachable knobs turns the **most common fingerprint in the
  world** into a unique one — from hiding in a crowd to being the only sender. So none of
  them are tuned.

Disguise failure is **completely silent**: drop the `core/ja3` blank import and everything
still compiles, tests pass and requests return 200 — they just go out as Go. Any service
relying on it needs a wiring guard asserting `outbound.Status()` reports no
`no-fingerprint` identity.
