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
That fail-open happens on *every* selection during the cooldown, so its warning is
throttled per service set (`failOpenLogEvery`, one line a minute carrying the count it
swallowed): a single-node owner cold-starting for a few minutes used to log hundreds of
identical lines per peer process.

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

**X-Forwarded-\* must survive a second proxy hop.** `httputil.ReverseProxy`'s `Rewrite` hook strips `X-Forwarded-*` from the outbound request and `SetXForwarded` rebuilds them from *this* hop's inbound request — so `X-Forwarded-Host` becomes `In.Host`. One hop (gateway → owner) is fine; two hops (gateway → websvc → kylin, which is how vectors' cross-process site-app mount dispatches) lose the original host, because the first hop already rewrote `Host` to its upstream address. Measured 2026-09-29: kylin rendered the portal page with `og:image` / `hreflang` pointing at `http://127.0.0.1:36399` (websvc's internal address) instead of `http://localhost`; on a domain-per-tenant deployment the owner would resolve the **wrong tenant**, and `X-Forwarded-Proto: https` and the client IP chain were dropped the same way. `setForwarded` now keeps the previous hop's Host/Proto and appends to `X-Forwarded-For` **only on an internal hop** — the inbound request carries the hop-depth header *and* comes from a loopback/private address (`internalHop`). The edge hop is unchanged byte for byte (client-supplied `X-Forwarded-*` are ignored). Regression: `router/reverse_proxy_forwarded_test.go`.

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

An erroneous reclamation is self-healing: the next **forced** `store()` rebuilds the route
(see the next paragraph — within 10 minutes, no longer every 60s). Not reclaiming is permanent. That is the same bias as `isLocalService` — prefer one extra pass
over a bad route nothing will ever correct. Regression:
`router/proxy_route_reclaim_test.go`.

**Unchanged records are not rebuilt** (`router/proxy_route_skip.go`). `refresh()` used to
`store()` every remote service every 60s whether or not anything changed, and the cacher
copied every record two or three times on the way (`Copy` before checking the TTL, again
into the cache, again on return). The registry is dominated by kylin's records (2026-10-23:
308 entries, 8.5MB, 7.6MB of them kylin's 154 services each carrying its full endpoint
table), so every process that does *not* host them — connect_components, websvc — rebuilt
thousands of routes a minute: measured 10.7GB allocated in 3 idle hours, ~32MB per round,
`registry.Copy` alone 4.3GB against 0.55GB of actual etcd decoding. kylin skips
its own services via `isLocalService`, which is why only the other two showed it. Now:

- `storeIfChanged` fingerprints exactly what `store()` reads (name, version, node id /
  address / metadata, endpoint path + methods, this process's identity) and skips when it
  matches the last *successful* build. Deferred rounds record nothing; `dropProxyRoutes`
  and the reconcile pass clear the fingerprint, so a service that comes back identical is
  rebuilt. Every `proxyForceEvery` (10) rounds is a full rebuild — that is the self-heal.
- The cacher copies under the lock and only when handing out: `update()` mutates cached
  entries **in place** (`services[i] = …`, `service.Nodes = …`), so a copy outside the lock
  is a race (`TestCacher_CopyDoesNotRaceWatchUpdate` goes red under `-race`). The cache
  keeps the backend's object; callers always get their own copy — previously singleflight
  handed the *same* slice to every concurrent caller.

Regressions: `router/proxy_route_skip_test.go`, `registry/cacher_copy_test.go`.

`registry.Service.Equal` decides (via `TRouter.isLocalService`) whether `refresh()` skips a service entirely, so a wrong `true` means **permanently no route**. It compares name, version, node count, and node ids — historically the whole comparison sat inside `if len(a.Nodes) == len(b.Nodes)` and fell through to `return true` when counts differed, which any zombie node (SIGKILL leaves the etcd lease un-revoked, so old+new nodes coexist and the count doubles) turned into "remote service misread as local". Keep the bias toward `false`: a false negative only costs one extra `store()` pass, where `filteSelf` filters precisely.

**A local route always wins over a discovered proxy route.** `filteSelf` is a *best-effort* filter — it only works when the process can recognize itself, and in the real vectors topology it routinely cannot: one `TRouter` is shared by the HTTP and the RPC server, each `server.WithRegistry` wraps the registry in its **own** `registryCacher`, and the router keeps only the last one — so `LocalServices()` never sees the sibling server's node, and every self-hosted service comes back through discovery looking remote (kylin logs one shield warning per service at startup; that is this, not a new fault). Three defenses now sit behind it, in `store()`:

- **`hasLocalRoute` (the shield).** An endpoint already served by a local handler never gets a proxy route. Without it, `TTree.addNode`'s last-writer-wins evicted the local handler *permanently* — `refresh()` afterwards classifies the service as local and skips it, so nothing ever repairs the tree. 2026-08-01, kylin: `/gamification/static/security/*.xml` answered 508 forever, and since module install/upgrade fetches manifest files over `GET /{service}/static/{file}` (`core/download`), the whole upgrade died with `prefetch module data: ... Failed to download data file`. Which modules got hit changed on every restart — it is a race, so "it works for account, therefore routing is fine" proves nothing.
- **`filteHttpNodes`.** HTTP proxy routes are only built from nodes that can actually speak HTTP (`metadata["protocol"]` http/https; unlabelled nodes are kept). The http and rpc registrations of one process carry the **same full endpoint table**, so a record left holding only the RPC node still lists the HTTP paths — and `getService` then re-selects an http node at request time, which is this process's own port. Route built from one node, dialed to another: instant self-loop.
- **`registryCacher.Register` records local identity *before* publishing.** The registry write is what wakes this process's own watcher, which drives `store()`; updating `localServices` afterwards leaves a window where the process cannot recognize its own brand-new registration. `LocalServices()` also keeps only *our* nodes out of the registry's merged read-back — pulling in a peer's node would make `filteSelf` drop a genuinely remote node, i.e. permanently no route, the same damage mirrored.

Regressions: `router/store_local_route_shield_test.go`, `registry/cacher_register_order_test.go`.

(The former `handler.Services`/`SetServices` field — once documented here as a data race — is gone: it had no readers and `SetServices` was never called, so the race could not occur. Removed rather than locked.)

## Descriptors: the registry holds a hash, the owner holds the bytes

Design and measurements: `docs/registry_descriptor_design.md`. A record used to be node
(changes per process) + descriptor (metadata and the endpoint table, changes per build), and the
descriptor was 99% of it — so one sibling restart made every reader re-transfer, re-decode and
re-parse 8.5MB. Now `registry.Use()` wraps every backend in a resolver (`registry/resolver.go`):

- **Register strips** Metadata/Endpoints down to `metadata["volts.desc"] = sha256(canonical bytes)`
  and keeps the bytes in a process table. Only when this process answers descriptors
  (`SetDescriptorServing`, set by a TCP server at construction) — an HTTP-only process keeps
  writing full records, nobody could fetch its descriptor. Records without metadata/endpoints
  (broker subscriptions) are never stripped.
- **Reads resolve**: own table → descriptor cache → batch fetch over the reserved RPC endpoint
  `registry.DescribeEndpoint` (`server/describe.go`, TCP side only, in an `IsService=false` group
  so it is never advertised). The fetch is injected by `client` (`client/describe.go`) because
  registry cannot import client. Fetched bytes must hash to the digest and name the referencing
  service; the record's signature covers the digest (it is in metadata), so any node may serve it.
- **Unresolvable = not visible.** A record whose descriptor cannot be fetched is left out of the
  result (`GetService` → `ErrNotFound` if nothing is left); the same digest is not re-fetched for
  2s. A watch create/update that cannot be resolved is skipped (the next refresh picks it up) —
  handing out an endpoint-less record would look like "all endpoints gone". Deletes pass with
  `Digest` set.
- **Group by `registry.GroupKey`** (digest, else `EndpointsKey`) — stripped records have empty
  endpoint tables, so `EndpointsKey` alone would merge two processes with different descriptors.
- **Metadata and Endpoints of a resolved Service are shared and read-only.** `CopyService` no longer
  copies Endpoint structs (it was the bulk of `registry.Copy`); nodes are still copied — clients
  rewrite `node.Address`.
- Old full records (no `volts.desc`) pass through untouched, so mixed binaries work and the etcd
  prefix is unchanged. vectors' record signature needed no change: it covers metadata.

Regressions: `registry/descriptor_test.go`, `internal/test/client/describe_test.go`.

## Deregister removes every service the server registered

One server registers many services (one per vectors module — kylin alone writes 127 per node).
`TServer.Deregister` used to remove only `config.Name`; every other record stayed until its lease
expired (etcd: 90s), so after each restart readers still saw the stopped node and the selector
could pick it, and the resolver never released those descriptors (`unpublishLocal` runs in
`Deregister`). `Register` now records every name it wrote (`registeredNames`) and `Deregister`
removes each one, continuing past failures.

None of this ran until 2026-10-23 anyway: `Run` never subscribed to signals (`Config.Signal` was never
set), so SIGTERM killed the process before `Stop`. Both fixed; on the local stack a restart's old
records now vanish in 1–3s (cc 3+3, websvc 24+24 records) instead of 80–100s.

Regression: `server/deregister_test.go`, `signal_test.go`.

**Backends must carry service-level metadata and key instances per (service, node).** Found
while doing the above (2026-10-23):

- consul used the node id as the consul service id. Every service of one process shares that node
  id, so each registration overwrote the previous one and one deregister removed them all. The id
  is now `<service>~<node id>` (`serviceID` / `nodeIDOf`). The hash/last-checked maps are keyed by
  it too; keyed by name, a process's http and rpc nodes flip each other's hash and re-register
  every round.
- consul (tags) and mdns (TXT) wrote only **node** metadata. Service metadata — where the descriptor
  digest lives — never left the process, so a stripped record read back as an empty service. consul
  now writes `s-` tags, mdns a `ServiceMetadata` field; both group by version + `registry.GroupKey`.

Regressions: `registry/consul/service_id_test.go`, `registry/mdns/mdns_test.go`
(`TestEncoding_CarriesServiceMetadata`).

## Record signing: who wrote this entry?

`registry.SetRecordSecurity(&RecordSecurity{Sign, Verify})` (`registry/record_security.go`)
is process-wide, like `transport.SetPeerTLS`. The registry decides where traffic goes —
selector dials node addresses, the gateway builds reverse-proxy routes from endpoints, the
application reads ownership and method contributions from service metadata — so anyone who
can write etcd could otherwise insert themselves into someone else's call chain.

- **Sign** runs on a single-node record before it is written. `SignedNode` hands it a
  **copy** of the node: one `server.Register` call shares one `*Node` across every service,
  and signatures are per service. Signing happens **before** the node hash, so a change in
  service metadata / endpoints now re-writes the entry (before, only node changes did).
- **Verify** runs on every record read back, **before merging** — after a merge only one
  copy of metadata / endpoints is left and it no longer matches each node's own signature.
  A failing record is dropped from `GetService` / `ListServices` / watch puts (logged,
  throttled). Watch **deletes are not verified**: deleting an entry that was never accepted is
  a no-op, and blocking the delete of one that was would keep it cached for a TTL.
- Only the etcd backend calls the hooks.

Regressions: `registry/record_security_test.go`.

## etcd backend: no read-only Txn

etcd **3.5.4** (the brew build on the dev machine) applies a read-only Txn on the request path
with the client's ctx. If the client times out or cancels mid-apply, `applyTxn` gets
`context canceled` / `deadline exceeded` and **panics the whole etcd process**
(`apply.go:638 "unexpected error during txn"`). Under load clients time out, etcd dies, every
service fails to register, the retries pile on — 2026-10-14 saw 14 crashes in one day, all this
stack, all from `rangePrefix`'s two-Range Txn. A plain Range that gets cancelled just returns an
error.

So: when you need several reads from one snapshot, do separate `Get`s and pin the later ones
with `clientv3.WithRev(<first response's Header.Revision>)` — same consistency, no Txn.
Txns that carry a write (Compare + Put, as `vectors/cmd/id_node.go` does) go through raft and are
not affected. Upgrading etcd past 3.5.4 removes the hazard but this rule stays: deployments
don't get to pick their etcd.

Regression: `registry/etcd/register_rewrite_test.go` `TestRangePrefix_ReusesUntilSomethingChanges`
(live etcd, test-only prefix).
