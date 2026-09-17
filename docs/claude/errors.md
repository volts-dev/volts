# 错误带着码，而码必须穿得过包装

## Errors carry a code, and the code must survive wrapping

`errors.Error` is the framework's error type: `Id`, `Code`, `Detail`, `Status`. Upper layers
(vectors' `codec.WriteData`) read `Code` to decide what status the caller sees, and anything
they cannot recognise is flattened to 500.

- **Read the code with `errors.Code(err)` / `errors.From(err)`, never a type assertion.**
  Business code wraps freely (`fmt.Errorf("...: %w", err)`), and `err.(*errors.Error)` misses
  the moment anything wraps it — the caller then gets a 500 for what was a 404. `Code` walks
  the chain.
- **`errors.Wrap(err, id, code, detail)` keeps the cause reachable**, so `errors.Is` can still
  identify a sentinel underneath. The cause is unexported: it is *not* serialized, so what
  crosses a process boundary is still only the four fields.
- **`(*Error).Is` compares Id + Code + Detail**, so a sentinel survives a JSON round-trip
  through RPC — pointer equality does not, and `Parse` builds a new object every time. All
  three fields matter: `PermissionDenied` and `UnavailableService` share Id `""` and Code 500
  and differ only in Detail.
- The package re-exports stdlib `Is` / `As` / `Join` / `Unwrap`, so a file that imports
  `volts/errors` does not also need the stdlib one under an alias.
