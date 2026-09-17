# 注册中心、节点健康与路由更新

## Node Health / Circuit Breaking

`selector` ejects a node after `BreakerFailures` consecutive failures (default 3), holds it
out for `BreakerCooldown` (default 10s, exponential backoff up to 2min), and fully restores
it on one success. `WithoutBreaker()` disables it.

Two rules that are easy to get wrong:

- **Callers must report both outcomes.** `Selector.Mark(service, node, err)` is the only
  input the breaker has. All three call sites (`client/http_client.go`,
  `client/rpc_client.go`, `router/reverse_proxy.go`) report success *and* failure — an
  earlier version called `Mark` only on the success path, where `err` was always nil, so
  the breaker never saw a single negative signal.
- **Reverse proxy counts connection-level errors only.** A 5xx from upstream means the node
  is reachable; ejecting on it would escalate a business error into lost capacity.

When every node of a service is tripped the filter **fails open** and returns them all: a
breaker that removes 100% of capacity is worse than the failure it guards against.

## Endpoint-aware node selection

A service name can be served by **more than one process**, and those processes do not
necessarily expose the same endpoints: another binary blank-imports the module (and
therefore registers the same service name) while being compiled earlier, or a rolling
upgrade has old and new binaries live at once. `registry.Service` carries `Endpoints`
per **registration entry**, but `etcd.GetService` used to merge entries by `Version`
alone — nodes were all appended while `Endpoints` kept only the *first* entry's. The
node↔endpoint mapping was gone, so `RpcClient` round-robined onto a process that does
not implement the path and got `rpc status NotFound: server returned no detail`, which
is indistinguishable from "this deployment does not have that module" and leaves both
sides' logs clean. Measured on the real stack (2026-09-03, vectors): ~50% of calls to
one endpoint failed, alternating.

Two halves, both required:

- **`registry.EndpointsKey`** — merging is keyed by version **plus** a fingerprint of
  the endpoint set (`GetService` and `registryCacher.update`). Processes with identical
  endpoint sets — one binary's replicas, i.e. the overwhelming majority — still merge
  into a single `Service`, so the shape only changes when it actually has to. Splitting
  costs nothing at balance time: the strategies flatten every `Service`'s nodes before
  picking (`selector/strategy.go`). `ListServices` deliberately does **not** use it —
  that call answers "what services exist", where a duplicated name would only make
  callers double-count.
- **`selector.FilterEndpointIfKnown`** — `RpcClient.next()` now filters candidates by
  the endpoint being called. It **fails open** when nobody advertises that name:
  endpoint naming is the caller's convention (volts itself uses `/model.method`) and
  older registrations may not report endpoints at all, so a strict filter would turn
  "I don't recognise this name" into "the service is unavailable" — the same trade-off
  as the breaker's all-tripped case. The test is "someone advertises it and you don't",
  never "you don't advertise it".

`registryCacher.Register` must keep scanning past entries whose nodes are not ours: a
`(name, version)` can now match several `Service` records, and a sibling process's may
come first — `break`ing on it would register an empty node set as "self", after which
`filteSelf` no longer recognises this process's own nodes.

Regressions: `registry/endpoints_key_test.go`, `registry/cacher_endpoint_split_test.go`,
`selector/filter_test.go`.

## Registry-Driven Route Updates

`router/router.go:store()` is called on Registry watch events and periodic refresh (every 60s) to update proxy routes pointing to discovered services. Discovered nodes that resolve to **this process itself** are dropped by `filteSelf` (matched by `node.Id` **or** `addr.LocalFormat`-normalized address, so a same-address / different-id node from a `Debug()` port collision is still caught); otherwise a self-referential proxy route would replace the local handler and loop back into `127.0.0.1:<own port>` until ephemeral ports exhaust (`connect: can't assign requested address`). `router/reverse_proxy_loopguard.go` is the registry-independent runtime backstop: a hop-depth header caps any forwarding cycle, and a self-address short-circuit (via `http.LocalAddrContextKey`) refuses to dial the process's own listen port. That short-circuit matches loopback **and** any local NIC address — the registry stores NIC addresses (`192.168.x.x:35999`), and when `LocalFormat`'s fold-to-loopback misses (10s IP cache vs. a network switch, or a zombie node advertising a stale IP) a loopback-only check is bypassed entirely.

**Only HTTP endpoints get proxy routes.** `store()` skips `CONNECT` (RPC) endpoints: there is no RPC reverse-proxy implementation — `RpcReverseProxy` was an empty function body, so the route silently swallowed the request and the caller waited out `RequestTimeout` (20s) for a `context deadline exceeded` with nothing in the log pointing back here. Worse, `TTree.addNode` replaces same-path routes, so that empty handler would evict the local RPC handler and turn working local calls into black holes. Cross-process RPC dials the target node directly via `client` and never transits the gateway, so nothing needs proxying. A request for a service this process doesn't host now gets `StatusNotFound` immediately.

**Discovered proxy routes are now reclaimed.** `store()` used to only ever add, and
`TRouter.Deregister` has no production caller at all, so a service whose last node
deregistered kept its proxy routes forever — the path answered **503** ("retry later", which
clients and gateways duly do) where the honest answer was 404. `router/proxy_route_reclaim.go`
books what `store()` actually built, per service, together with the proxy handler id, and
removes the difference on the next round, on a watch `delete`, and on a `refresh()`
reconciliation against `ListServices`.

Removal is safe because `TTree.DelRoute` strips **by handler id** (`route.stripHandler`) and
only drops the node once nothing is left on it; the id is a crc32 of `service:path:…`, so it
cannot collide with a local handler or another service. Two further gates sit on top, and
only one of them is load-bearing:

- **A path still claimed by another service is never removed** — this one matters. Two
  services advertising one path share a single tree route (last writer wins), so removing the
  later one would take the path away from the earlier one until the next refresh, up to 60s.
  The regression must drop *the service whose handler is currently on the tree*; dropping the
  other proves nothing, because the id-recheck has already given up by then.
- **The id is re-checked against the tree before deleting** — defensive only. Missing the
  strip is harmless; the check just avoids a pointless write-locked traversal and keeps
  `TTree.Count` (a display counter) honest.

An erroneous reclamation is self-healing: the next `store()` rebuilds the route. Not
reclaiming is permanent. That is the same bias as `isLocalService` — prefer one extra pass
over a bad route nothing will ever correct. Regression:
`router/proxy_route_reclaim_test.go`.

`registry.Service.Equal` decides (via `TRouter.isLocalService`) whether `refresh()` skips a service entirely, so a wrong `true` means **permanently no route**. It compares name, version, node count, and node ids — historically the whole comparison sat inside `if len(a.Nodes) == len(b.Nodes)` and fell through to `return true` when counts differed, which any zombie node (SIGKILL leaves the etcd lease un-revoked, so old+new nodes coexist and the count doubles) turned into "remote service misread as local". Keep the bias toward `false`: a false negative only costs one extra `store()` pass, where `filteSelf` filters precisely.

**A local route always wins over a discovered proxy route.** `filteSelf` is a *best-effort* filter — it only works when the process can recognize itself, and in the real vectors topology it routinely cannot: one `TRouter` is shared by the HTTP and the RPC server, each `server.WithRegistry` wraps the registry in its **own** `registryCacher`, and the router keeps only the last one — so `LocalServices()` never sees the sibling server's node, and every self-hosted service comes back through discovery looking remote (kylin logs one shield warning per service at startup; that is this, not a new fault). Three defenses now sit behind it, in `store()`:

- **`hasLocalRoute` (the shield).** An endpoint already served by a local handler never gets a proxy route. Without it, `TTree.addNode`'s last-writer-wins evicted the local handler *permanently* — `refresh()` afterwards classifies the service as local and skips it, so nothing ever repairs the tree. 2026-08-01, kylin: `/gamification/static/security/*.xml` answered 508 forever, and since module install/upgrade fetches manifest files over `GET /{service}/static/{file}` (`core/download`), the whole upgrade died with `prefetch module data: ... Failed to download data file`. Which modules got hit changed on every restart — it is a race, so "it works for account, therefore routing is fine" proves nothing.
- **`filteHttpNodes`.** HTTP proxy routes are only built from nodes that can actually speak HTTP (`metadata["protocol"]` http/https; unlabelled nodes are kept). The http and rpc registrations of one process carry the **same full endpoint table**, so a record left holding only the RPC node still lists the HTTP paths — and `getService` then re-selects an http node at request time, which is this process's own port. Route built from one node, dialed to another: instant self-loop.
- **`registryCacher.Register` records local identity *before* publishing.** The registry write is what wakes this process's own watcher, which drives `store()`; updating `localServices` afterwards leaves a window where the process cannot recognize its own brand-new registration. `LocalServices()` also keeps only *our* nodes out of the registry's merged read-back — pulling in a peer's node would make `filteSelf` drop a genuinely remote node, i.e. permanently no route, the same damage mirrored.

Regressions: `router/store_local_route_shield_test.go`, `registry/cacher_register_order_test.go`.

(The former `handler.Services`/`SetServices` field — once documented here as a data race — is gone: it had no readers and `SetServices` was never called, so the race could not occur. Removed rather than locked.)
