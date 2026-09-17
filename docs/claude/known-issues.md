# 已知问题与两轮审计的留痕

## Known Issues (tracked in memory)

The original 20-item concurrency/correctness audit (see project memory) has been fully
dispositioned as of 2026-07-17 — every CRITICAL/HIGH item and each named MEDIUM item is
either fixed or verified already-fixed in current code (WebSocket proxy leak, context
pool race, SIGKILL, `registered` field, `handler.Services` lock, pool-key growth via
`TTree.onRouteReplaced`, router lifecycle via `TRouter.Close()`, Stop error aggregation,
broker/http Close deadlock + transport double-close).

A second, wider audit (security / stability / efficiency / stack) ran 2026-07-20..21 —
21 items, all actionable ones fixed on branch `fix/security-audit-p0`. Acceptance:
`go test -race ./...` clean, `go vet ./...` clean, 3 fuzz targets ~5M execs with no crash.
See project memory for the full list. Remaining open notes:

- **config hot-reload's read side is now synchronized** (`config.WithWatcher()`, which
  vectors enables in production). Reload decodes into a *copy*, validates it, and writes
  back **only the fields that actually changed** — so most reloads write nothing at all,
  an invalid file no longer corrupts a live instance, and the config layer's own write
  paths (Register / LoadFromFile / Reload) are serialized by `core.applyMu`.
  The remaining read-side race is closed by `config.HotRLock` / `HotRUnlock`: a
  process-wide `RWMutex` taken for writing by `applyChangedFields` — and **only when a
  field actually changed**, so a no-op reload never blocks a request — and for reading by
  the accessors that sit in front of every hot field.
  Two things made this far smaller than it looks. The set of fields that are both
  reloadable and read on a request/connection path is **five**, not "every read site":
  `router.Config.Recover` / `RequestPrinter` and `selector.Config.BreakerFailures` /
  `BreakerCooldown` / `BreakerMaxCooldown`. And `transport.Config` / `server.Config` had
  already taken the other valid route — their `OnReload` returns `ErrReloadUnsupported`,
  so nothing writes them at all and there is no race to close; adding a lock there would
  guard a writer that does not exist.
  The accessors read the whole group in one acquisition (`breakerSettings` returns all
  three) because reading them one at a time straddles a reload and yields a mix of old and
  new. `RWMutex` read locks are **not reentrant** — a second `RLock` on one goroutine
  deadlocks if a writer queues in between — so the accessors are called in sequence, never
  nested, and an `OnReload` implementation must not call them. Measured on
  `BenchmarkServe_3Mid`, `-count=9`: 1411 ns/op with the accessors vs 1455 without, i.e.
  below this benchmark's noise floor.
  Regressions: `config/reload_race_test.go` (needs `-race`; a torn bool read is
  unobservable otherwise) and `config/hot_field_guard_test.go`, a source scan that fails
  if any of those files reads a hot field directly again — the list in that test *is* the
  list.
  Reload must keep skipping `field:"-"` when diffing — those are live instance pointers
  (Logger / Registry / Transport / Router); `UnmarshalKey` never populates them, so the
  copy holds nil, and diffing them would "detect a change" and null out the running
  registry and transport. Regression: `TestReload_OnlyWritesChangedFields`.
  A model can opt out or take over via `config.IReloadable`: returning
  `config.ErrReloadUnsupported` makes the framework touch **nothing** (the only way to get
  the race to zero — right for anything that needs a restart to take effect anyway, like
  listen addresses or DB connection strings); returning nil means the model applied the
  change itself under its own lock. The change list is computed *before* `OnReload`, since
  afterwards the model already equals the new copy and nothing would diff.
- **go.mod must stay free of `replace`.** A local `replace ... => ../xxx` only holds on
  one machine; left in the repo it means nobody else can `go get` this module, and the
  error they see points at *their* disk. The cacher replace sat here for months doing
  exactly that. Temporary replaces are legitimate — `TestGoMod_HasNoReplaceDirective`
  makes forgetting to remove one visible immediately instead of when someone reports that
  the library will not install.

- `IClient` interface completeness is a design gap, not a bug. It lacks `NewRequest`
  because HttpClient (`method, url, data`) and RpcClient (`service, method, request`)
  differ semantically — code needing it must hold the concrete type.
- broker/http `Publish` must keep `maddr.LocalFormat` on node addresses (macOS network
  extensions intercept traffic to physical interfaces — same rule as client/reverse_proxy)
