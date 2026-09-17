# 日志：IWriter 管格式，Sink 管收集

## Logging: `IWriter` formats, `Sink` collects

Two extension points, and picking the wrong one is why a "ship the logs somewhere"
feature ends up only half working:

- **`IWriter`** receives one **already formatted** line (`[prefix][ERR] …`, possibly
  prefixed with `[file:line]`) and is attached **per logger**. Since every package calls
  `logger.New(prefix)` on its own, nothing knows the full set — so a component that wants
  *this process's* logs cannot get them this way.
- **`logger.AddSink(s)`** (`logger/sink.go`) is **process-wide** and **structured**: every
  logger's every line reaches it as an `Entry` (level, logger name, message with the level
  tag stripped, caller file/line), so nobody has to reverse-engineer the formatted string.
  It returns a `remove` func; adding/removing is safe at runtime (snapshot swap, no lock on
  the write path).

Three rules the sink contract depends on:

- **`Write` is synchronous and must be cheap.** It runs on the goroutine that logged, and
  **before** the async channel — otherwise a sink would only see what the channel didn't
  drop, and what it drops is precisely the busiest, most interesting stretch. So `Write`
  may only enqueue; do I/O on your own goroutine. A panic inside a sink is swallowed and
  cannot take the caller down.
- **`Wants(level)` is the first gate.** The `Entry` (including `runtime.Caller`) is only
  built when at least one sink wants that level, so a warn-and-above sink costs one atomic
  load plus one interface call on `info`/`debug`.
- **The logger's own level filter runs first.** A sink sees exactly what the process would
  have printed — no more, no less.

**Compare severity with `Level.Severity()`, never with the `Level` value.** The numbers are
RFC5424-ish plus two local levels and are *not* monotonic in severity:
`Info(5) < Warn(6) < Error(7) < Trace(8) < Debug(9)`. "Error and above" written as
`lv >= LevelError` silently includes trace/debug and excludes alert/critical.
`Severity()` (debug 0 … attack 8) is the real order; `ParseLevel` reads the names.

Caller resolution skips frames by **package name** (`…/logger.Func`), not by a fixed depth:
upper layers wrap this package with their own `logger` package (vectors does), and a fixed
depth necessarily points at the wrong line for one of the two call paths.
