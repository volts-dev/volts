# 配置树＝结构体图

## Config: the tree is the struct graph

**A component's config is a plain struct. Its owned children are fields. Shared things are
names.** That one sentence replaces what used to be a `config.Config` embedded in every model,
a hand-written `String()` per package computing `prefix.kind` / `kind.name`, and a
`WithConfigPrefixName` that each parent forwarded to the children it happened to know about.
The measured result of the old scheme, on kylin's real config.json: 464 leaves, ~120 sections,
one etcd registry's config copied under 20+ paths, and
`server.http.broker.router.selector.registry` five levels deep.

```go
// transport/config.go — a plain struct; exported fields are config, interface/func fields
// (Listener, ACMEProvider, Context) are instances and are skipped automatically.
type Config struct {
    Name        string `field:"-"`   // only the path segment, not a config key
    Addrs       string
    ReadTimeout time.Duration        // untagged multiword → key read_timeout, both directions
    Listener    IListener            // interface: not config, no tag needed
    use         *Config              // WithConfig marker
}

// server/config.go — owns its transport, references shared things by name
type Config struct {
    Name            string            `field:"-"`
    Address         string
    TransportConfig *transport.Config `field:"transport"` // owned → server.http.transport.*
    RegistryType    string                                // reference → registry.<type> is its own root
    Transport       transport.ITransport                  // instance, skipped
    Router          vrouter.IRouter                       // shared instance, skipped
}

func newConfig(opts ...Option) *Config {
    cfg := &Config{ /* code defaults */ }
    cfg.Init(opts...)                       // options only write fields
    cfg.TransportConfig = cfg.Transport.Config() // or transport.NewConfig() when none injected
    config.Bind("server."+cfg.Name, cfg)    // the one and only tree call
    // children constructed *after* Bind take the file's values:
    transport.NewHTTPTransport(transport.WithConfig(cfg.TransportConfig))
}
```

Rules that fall out of it, each of which replaced a bug:

- **`Bind(path, ptr)` does three things, once:** seeds the struct's current values as
  *defaults* (viper's defaults layer, below the file), decodes the file's section into the
  struct (with `${VAR}` expansion), and registers the root. Nested structs — value or non-nil
  pointer — are subsections at `path.<key>`; `time.Time` is a leaf. `config/walk.go` is the
  single walker every path uses (defaults, decode, reload diff, validate, lint, env), so
  "what keys a struct declares" and "what it writes/reads" cannot disagree. The old code had
  three copies of that rule and they did disagree: untagged multiword fields were written
  as `selectorstrategy` and read as `selector_strategy`.
- **What is config is decided by type, not by tag.** Exported + not interface/func/chan +
  no `field:"-"`. Instance references (`Logger`, `Registry`, `Transport`, `RecoverHandler`)
  need no tag. Pointers to foreign structs that are *not* config (`*tls.Config`) and plain
  strings used only as path segments (`Name`) still take `field:"-"`.
- **Owned child = field; shared instance = name.** The test for "owned" is *who Stops it*.
  server owns its transport (`server.http.transport`), router and client own their selector
  (`router.default.selector`, `client.rpc.selector`), client owns its transport. router,
  registry and broker are shared across servers and are their own roots (`router.default`,
  `registry.etcd`, `broker.http`); the server holds `RegistryType`/`RegistryHost`,
  `BrokerType`/`BrokerHost`. The registry cacher's config is deliberately **unbound**
  (`registry.newCacheConfig`) — it is an implementation parameter, and binding it under every
  holder's prefix is exactly what produced the 20 copies.
- **Owner-constructed children use `WithConfig`.** `selector.New(selector.WithConfig(cfg.SelectorConfig), …)`
  makes the instance use the struct the parent already bound; options given alongside are
  applied to that struct; nothing is bound again. `router.New(router.WithConfig(router.NewConfig()))`
  is the idiom for a *private* component that must not appear in the file (broker/http's
  internal delivery router).
- **Injected children are adopted.** `transport.NewHTTPTransport()` binds itself at
  `transport.default`; when `server.WithTransport(t)` hands it in, the server's `Bind` finds
  that pointer among its nested fields and **removes the old root**. From then on only
  `server.http.transport.*` reaches that instance. Regression:
  `server/config_tree_test.go` — the judgement is behavioural (rewrite `transport.default`,
  the injected transport must not move), because the broker legitimately keeps the shared
  `transport.Default()` bound at `transport.default`.
- **Decoding takes the file layer plus runtime overrides, never `viper.Get(section)`.**
  viper searches layer by layer and returns the *first* layer's whole subtree, so one
  `SetValue` under a section hid every other file key of that section on decode.
  `client.Config.Init` used to `Save()` every field into the override layer on every option
  — config.json's `client.*` section had never taken effect. `format.decodeSection` keeps a
  file-only tree (`fileTree`) and overlays tracked override keys. Regression:
  `TestBind_OverrideDoesNotShadowFileSiblings` (SetValue *before* Bind is what makes the old
  path fail).
- **Rebinding the same path with the same type is silent** — every `HttpClient` is
  `client.default` unless named, and vectors builds them on pool miss. A different type on
  the same path logs once; it is the only genuine collision.

**The file is still a read-only input.** `Bind` seeds defaults into the defaults layer,
`LoadFromFile` never flushes, `SaveToFile` persists **only the diff** — values set through
`SetValue`, plus keys already in the file. `Dump()` is the one exit that emits defaults, for
generating an editable template (with secrets masked to `${VAR}` placeholders). Writing
defaults back is what made `if cfg.X == 0 { fallback }` dead code and copied
`registry.timeout` 15 times. Durations are milliseconds as bare numbers at every nesting
depth; unit-suffixed strings (`"5s"`) are preferred in files.

**Secrets can be externalized.** `${VAR}` is resolved from the environment at load time;
`${VAR:-default}` declares it optional. An unset `${VAR}` with no default is an **error**,
never a silent empty string. Expansion happens on the **struct only**: the tree keeps the raw
literal, so no `SaveToFile` can bake a resolved secret into the file. Regression:
`TestExpandEnvRefs_NotWrittenBackToFile`.

**Hot reload walks the same graph.** Each root is decoded into a deep *copy* (`cloneConfig`:
nested pointers reallocated, slices/maps copied — sharing a backing array would let a rejected
reload half-apply), validated recursively, and only the leaves that actually changed are
written, under `config.HotRLock`'s writer side. Any struct at any depth may implement
`IReloadable` (`OnReload(next any) error`): `ErrReloadUnsupported` freezes that subtree and
logs what was ignored; `server.Config` and `transport.Config` do this, so a reload never
touches them while `router.Config.Recover` beside them hot-updates. Regressions:
`config/reload_test.go` (nested cases: `TestReload_WritesNestedChildLeaves`,
`TestReload_NestedChildCanRefuse`), `config/reload_race_test.go` (needs `-race`),
`config/hot_field_guard_test.go` (source scan: the five hot fields must be read through
accessors).

**Diagnostics.** Load errors accumulate (`config.Errors()`) instead of `log.Fatal` — a library
must not `os.Exit` on the caller's behalf. `service.Start()` reports errors, `Lint()` and
`Validate()`, and prints `Inspect()` when `VOLTS_CONFIG_REPORT` is set. `config.Bound()`
lists the roots.

**`Lint` reports a key only when its parent path is a struct node** (a root, or a nested struct
field of one). That single rule, read straight off the graph, replaces the old string-based
reverse engineering of "which model declared this prefix": a map field's keys are not typos,
a section nobody bound is not a typo (configs bind **lazily** — only components that were
actually built are on the tree), and a subtree left behind by a moved component is not a typo.
Whole-section orphans are `Report`'s job. Regressions: `config/lint_test.go`.

**Validation is post-order over the graph.** Nested structs validate before their parent, roots
deeper in the path before shallower ones. Models return `config.Invalid("read_timeout", …)`
with a **relative** field name; the framework prefixes the full path and file line
(`config.json:47: server.http.transport.read_timeout: …`). A component cannot know where it is
mounted — that is the whole point of the design.

**Never name a `*Config` method after an interface the models might satisfy.** The legacy
embedding is still supported (below), so a tree-wide `Validate()` would be promoted onto
every legacy model and recurse into itself. Hence `ValidateAll` / `ReportAll` on the type,
`config.Validate()` / `config.Inspect()` as package functions.

**There is no embedding any more, and no shim.** `config.Config` is only the tree handle.
vectors' ten business configs (`db`, `auth`, `session`, `permcache`, `search`, `mailer`,
`pdf_printer`, `aliyunoss`, `service.storage`, `service.registry`, plus the `vectors` root) are
plain structs bound with `config.Bind` / `module.WithConfig(path, cfg)`; the eight
`client.WithConfigPrefixName("x")` call sites became `client.WithName("x")`, so their
sections moved from `x.client` to `client.x`. A struct that needs to act after every file
load (the db config re-pointing an already-built ORM at the file's datasource) implements
`IAfterLoad` (`AfterLoad() error`), which runs after `Bind` and `LoadFromFile` decode, child
before parent, and never on hot reload — that path is `IReloadable`'s.

**Migration notes for existing config.json files.** Sections that no longer have an owner
(`server.http.router.*`, `server.http.registry.*`, `router.default.registry.*`,
`*.selector.registry.*`, `<prefix>.client.*`) are now orphans: `Report` lists them, `Lint`
stays quiet. The values that mattered live at the roots (`registry.etcd`, `broker.http`,
`router.default`) and under the owners (`server.http.transport`, `client.<name>.transport`).
