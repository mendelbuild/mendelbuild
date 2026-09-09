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

A fixture carries its **database** as well as its files where it has one.
`ledger/schema.sql` is what its migrations would have left, and it is what the
experiment declared in its own `.mendel/experiment.json` is admitted against —
so the declaration, the schema and the code all have to agree, and a drift in
any of them fails rather than being discovered against a real project that
someone had redeployed.

That is the point of testing against these rather than against a live Mendel
project: a real project changes under you, so a failure is ambiguous between
"Mendel is broken" and "somebody deployed something", and there is no way back
to a known state. A fixture is the same every time.

`notes` and `pong` carry no schema, and that is an answer rather than a gap.
`pong` has no datastore; `notes` has one Mendel cannot adapt, which declines
before anything connects, so there would be nothing to apply a schema to.

None of these need Docker to be useful. A fixture is a repository on disk, and
most of what reads one — the `.mendel` specs, the declaration, the decline for a
datastore with no adapter — is a function over files and needs nothing running.
`docker-compose.test.yml` is here because a real repository would have one and
the specs are read against it, not because anything starts it.

`notes` is the important one and the easiest to leave out. §13's decline is a
designed outcome, not a failure path — "Mendel does not know a safe way to do
this against MySQL yet" is a good answer — and a fixture set containing only
datastores that work would test everything except that.

And `ledger` is Postgres because it is the engine with an adapter, not because
Postgres is the default. If a second adapter is ever written, the fixture for it
belongs here beside these and not instead of one.
