# Fixture user repositories

Small, self-contained repositories standing in for the thing Mendel actually
works on. They exist so the code paths that read a user's project — the
`.mendel/` specs, datastore admission, the decline when Mendel cannot help —
can be exercised without seeding an entire Mendel project first.

Under `testdata/` on purpose: it is the one directory name the Go toolchain
skips, so a fixture may carry its own source files, Dockerfiles and configs
without `go build ./...` trying to make sense of them.

Each repository earns its place by being a case some decision turns on, rather
than by being another example:

| Repository | Datastore | The case it is |
|---|---|---|
| `ledger` | Postgres | The whole path works: a schema change, verified and admitted |
| `notes` | MySQL | Mendel has no adapter, and must decline **by name** rather than approximate |
| `pong` | none | A presentation-only experiment, which must not be blocked on database requirements it does not have |

`notes` is the important one and the easiest to leave out. §13's decline is a
designed outcome, not a failure path — "Mendel does not know a safe way to do
this against MySQL yet" is a good answer — and a fixture set containing only
datastores that work would test everything except that.

And `ledger` is Postgres because it is the engine with an adapter, not because
Postgres is the default. If a second adapter is ever written, the fixture for it
belongs here beside these and not instead of one.
