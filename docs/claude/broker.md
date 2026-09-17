# 消息总线：http / memory / redis

## Subscribers: only `func(*TSubscriberContext)` can ever run

`handler.InvokeSubscriber` walks `handler.funcs` and calls `handle.SubFunc`, whose type is
`func(*TSubscriberContext)`. Anything that does not end up in `funcs` with that shape is
registered, advertised in the registry, delivered to this process — and never executed.

`parseSubscriber`'s two branches used to disagree about that. The plain
`func(*TSubscriberContext)` case filled `funcs`; the reflection case (a struct's methods, or a
func arriving as a `reflect.Value`) built the handler and returned without ever populating
`funcs`. So **struct subscribers silently never fired**, with `Subscribe` returning nil. Two
further faults in the same branch confirm nobody had ever taken it:

- The methods were collected with `Type().Method(i).Func`, which is **unbound** — its first
  parameter is the receiver, so the signature is `func(T, *TSubscriberContext)`, matching
  nothing and with no receiver to pass. `Value.Method(i)` is the bound form.
- `if v, ok := sub.(IString)` shadowed the type-switch's own `v`, so the `else` branch handed
  `utils.Obj2Name` the **failed** assertion's nil interface — `reflect: call of
  reflect.Value.Type on zero Value`. Any struct subscriber not implementing `IString` panicked
  on registration.

Struct methods now work, and must be **value receivers** with exactly that signature — the
same rule as controllers and middleware, for the same reason (the method set of a value
excludes pointer-receiver methods). Methods with any other signature are skipped, since a
struct normally has unrelated methods too.

`validateSubscriber` was `return nil`. It now rejects any handler with an empty `funcs`, so a
shape that cannot execute fails at registration instead of surfacing later as "that event does
nothing". Regression: `router/subscriber_test.go`.

## Broker: one instance per process

`broker.Use(name, opts...)` caches instances. The key is the **resolved** backend name
(an empty or unregistered name falls back to `http`), plus `name-addr` when the caller
supplied a real address. A caller with no usable address gets the bare-name slot — which
is how `broker.Default()` and a `server` with no `BrokerHost` come to hold the *same*
object. That sharing is load-bearing, not an optimization:

- **Only the server's broker is given a real registry** (`server/config.go`'s
  `WithRegistry` → `cfg.Broker.Init(broker.WithRegistry(r))`). A second instance keeps the
  `NopRegistry` it was born with, whose `GetService` returns `(nil, nil)` — so `Publish`
  iterates an empty node list, sends nothing, and **returns nil**. Both sides' logs stay
  clean; the only symptom is "the UI didn't update", which looks exactly like "the business
  event never fired". `server` therefore also calls `broker.SetDefault(bk)`, since library
  code reaches the bus through `broker.Default()`.
- **Only the server's broker is `Start()`ed**, so only it has a listening port. Subscribing
  on an unstarted broker would register a node whose port is `0`; `httpBroker.Subscribe`
  now `Start()`s first (it is idempotent) rather than publishing an unreachable address.
- A port of `0` is not an identity (`usableAddr`): `DefaultAddress` is `127.0.0.1:0` and
  `WithBroker` copies `bk.Address()` back into `cfg.BrokerHost`, so once that value lands in
  config.json the next boot would look like an "addressed" call and split the process again.

Sharing has a cost to know: the first `Stop()` closes the broker for every holder, and
`Address()` is read during a later server's assembly — hence the exit goroutine's address
restore must happen **under the lock and before the `ch` handshake**, or a `Close`→`Start`
sequence overwrites the newly bound address with `127.0.0.1:0`.

**Broadcast is a cross-process string contract.** A subscription without a queue registers
`registry.Service.Version = config.BroadcastVersion` (default `broadcast`, i.e.
`broker.DefaultBroadcastVersion`), and the publisher decides fan-out-to-all vs. pick-one by
comparing that string, so both sides must read one source — `httpBroker.broadcastVersion()`.
They used not to: the publisher compared a package var `"volts.http.broadcast"`, so **every**
broadcast subscription fell into the queue-group branch and went to one random node. It is
invisible in-process, because all subscriptions of one broker share a single `node.Id` and
one POST fans out to every local handler — which is why `TestConcurrentSubBroker` stayed
green. Regressions: `broker/http/broadcast_test.go`, `broker/use_instance_test.go`.

**The delivery response's status code is the only feedback channel a publisher has**, and
`broker/http/handler.go` gives it three meanings — get one wrong and the failure is silent
in a different way each time:

- **4xx = permanently refused** — unparseable message, no `v-topic`, or no subscriber with
  that `id` (the registry entry is stale: unsubscribed, or the process restarted with a new
  uuid). Retrying changes nothing, so the publisher drops it rather than replaying a batch
  of doomed messages ahead of every real one.
- **5xx = retryable**, kept in the backlog.
- **2xx = delivered — including when the handler returns an error.** The message *did*
  arrive; what failed is the application. Answering 5xx would push it back into the backlog,
  turning "a handler is broken" into "every subsequent message is consumed twice" — and
  delivery here is at-most-once, so callers owe no idempotency. Handler errors go to
  `Config().ErrorHandler` (which the http backend previously ignored entirely) or the log,
  per subscriber; a panicking handler is recovered so it cannot take its siblings' delivery
  down with it. All of this used to answer 200 unconditionally: a message could be sent,
  received by nobody, and leave both sides' logs clean.

Delivery is **at-most-once**: `event.Ack()` is a no-op in both built-in backends and
`SubscribeConfig.AutoAck` / `DisableAutoAck()` are not implemented by either — there is no
redelivery window to acknowledge. The retry backlog is 64 messages per topic in the
*publisher's* memory (`maxInboxSize`), drained 8 at a time and **only** by the next
`Publish` on that same topic — a topic nobody publishes to again never retries — and
overflow now drops the **oldest**, where it used to keep the oldest 64 and discard each
newly arriving message. Use this broker for cache-invalidation-shaped signals — the message
carries a key and the receiver re-reads the source of truth — not for messages that must
not be lost.

**Unsubscribing deregisters the node only when the topic has no subscribers left.** All
subscriptions on one topic share a single `node.Id` (`topic + "-" + broker uuid`) and hence
one registry record, so an unconditional `Deregister` silently removes the whole *process*
from that topic's delivery list. `Topic()` / `Config()` / `Unsubscribe()` were all stubs
before — `server.Stop()`'s unsubscribe loop never did anything, and `sub.Config().Queue` was
a nil dereference on the http backend while working fine on memory.

`Publish` treats `registry.ErrNotFound` as "no subscribers", not as an error: `NopRegistry`
returns `(nil, nil)` and memory/etcd return `ErrNotFound` for the same situation, so passing
it through made the same business code start reporting failures when the registry changed.

Regressions: `broker/http/delivery_test.go`.

## broker/redis: swapping the backend swaps the contract

`broker/redis/` keeps messages **in redis, not in the publishing process's memory**. Opt in
with a blank import, exactly like `registry/etcd` — it is deliberately absent from
`server`'s import list, so go-redis stays out of binaries that never ask for it:

```go
import _ "github.com/volts-dev/volts/broker/redis"
// server.WithBrokerType("redis"), server.WithBrokerHost("redis://:${REDIS_PASSWORD}@host:6379/1")
```

Three properties invert what the sections above document, so code written against the
built-in backends is not automatically correct here:

- **Delivery is at-least-once and `IEvent.Ack()` is real** (an `XACK`). `DisableAutoAck()`
  actually works. Callers now owe idempotency — the exact obligation the http backend
  explicitly does *not* impose.
- **Publisher and subscriber need not overlap in time.** The http backend's backlog is 64
  messages in the publisher's RAM, gone when it exits; here the backlog is the stream itself.
- **The registry is unused** (`WithRegistry` is a no-op). The whole "two mutually unknown
  broker instances, one holding a `NopRegistry`, silently dropping every message" failure
  class does not exist on this backend.

Semantics map onto consumer groups: one topic is one stream; subscriptions sharing a `Queue`
share one group (messages split across them); a subscription without a queue gets **its own**
group (every group sees everything).

Rules that are easy to get wrong:

- **Broadcast needs one group per *subscription*, not per process.** A group splits messages
  internally, so two local broadcast subscriptions sharing one group would each see half —
  where http's single POST fans out to every local handler. Group names carry a slot index
  (lowest free), so unsubscribe-then-resubscribe reuses the name, which is what lets a
  configured `WithBroadcastID` resume its read position across a restart.
- **Liveness is a heartbeat key, never the consumer's `idle`.** Measured on redis 7.0.4: an
  `XREADGROUP` that returns nothing does **not** refresh the consumer's seen-time (151ms →
  209ms across an empty read) and does not even register the consumer. So "no consumers /
  idle for a day" is precisely what a healthy low-traffic subscriber looks like, and an
  idle-based sweep destroys live groups. The damage is near-invisible: the read loop's
  `NOGROUP` branch rebuilds the group at `$`, so the symptom is "every so often a run of
  messages goes missing". Each broadcast subscriber therefore renews `<stream>:hb:<group>`
  (throttled to `GroupTTL/4`), and the sweep only destroys broadcast groups whose key has
  expired. Queue groups are never swept — they are shared, and this process cannot know
  whether another one still uses them.
- **`AutoAck` defaults to true, so build the config with `broker.NewSubscribeOptions`.** A
  hand-rolled `for range opts` over a zero `SubscribeConfig` — which is what `broker/memory`
  does, harmlessly, since it never reads the field — yields `AutoAck=false`: nothing is ever
  acked and every message is redelivered after `ClaimMinIdle`. It surfaces as "messages
  occasionally duplicate", never as a config error.
- **A handler error must NOT ack** — the opposite of the http backend's rule, because here a
  real redelivery window exists. `MaxDeliveries` (default 5) then bounds it: a message that
  can never be handled would otherwise hold the head of the PEL forever, with every genuine
  retry queued behind it.
- **`MaxLen` (default 10000, `XADD MAXLEN ~`) is a bounded buffer, not a durable queue.** A
  subscriber down long enough for its backlog to be trimmed has lost those messages. Same
  shape of guarantee as the http backend's inbox, just far larger and outside the publisher.

Tests spin up their own `redis-server` on a **unix socket** (no fixed ports; the path is kept
short because macOS `sun_path` is 104 bytes) and skip when the binary is absent.
`TestUnsubscribe_StopsDelivery` deliberately uses a *queue* subscription: destroying a
broadcast group unblocks `XREADGROUP` with `NOGROUP` immediately and the rebuilt group starts
at `$`, which masks a read loop that never stopped.

Regressions: `broker/redis/redis_test.go`.
