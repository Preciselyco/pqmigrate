# pqmigrate
[![FOSSA Status](https://app.fossa.io/api/projects/git%2Bgithub.com%2FPreciselyco%2Fpqmigrate.svg?type=shield)](https://app.fossa.io/projects/git%2Bgithub.com%2FPreciselyco%2Fpqmigrate?ref=badge_shield)

Migration utility for postgres databases

## No-transaction migrations

Some statements cannot run inside a transaction, most notably
`CREATE INDEX CONCURRENTLY` and `DROP INDEX CONCURRENTLY`. A migration file
whose **first line** is exactly

```sql
-- pqmigrate:no-transaction
```

(trailing whitespace allowed) runs outside a transaction in that direction. The
`.up.sql` and `.down.sql` files are marked independently. The directive is part
of the text stored in the migrations table, so a `down` taken from the database
is recognised too. `pqmigrate create --no-tx <name>` writes the directive into
both new files.

### Opt-in

Library callers must set `Config.AllowNoTransaction`. Without it, a call that
would run a no-transaction migration fails before running anything and names
the file. The CLI always allows them. A dry run (`Config.DryRun`, `-d`) that
includes a no-transaction migration also fails up front, because such a
migration can't be rolled back.

### Deferral

No-transaction migrations are **deferred** within a call:

- `MigrateUp(steps)` selects the same migrations as before (the first `steps`
  pending ones by version). It runs the transactional ones first, in version
  order, as usual. Then it commits that transaction, even with `AllInOneTx`,
  and runs the no-transaction ones in version order. `Config.AfterTxCommit`, if
  set, is called between the two, only when the run has both kinds.
- `MigrateDown(steps)` does the reverse. It runs the no-transaction `down`s
  first, then the transactional ones, each in descending version order.
- `MigrateUpFile` / `MigrateDownFile` commit any open transaction before running
  a no-transaction file.
- `Sync` defers the new migrations it applies the same way. It runs
  re-applied and removed migrations each in its own mode.

The deferral only reorders a no-transaction migration after the transactional
migrations **of the same run**. It is never moved past migrations outside the
run, so its versioned position only matters relative to those. Consequences:

- A no-transaction migration may depend on any transactional migration in the
  same run, even one with a higher version. A transactional migration must not
  depend on a no-transaction migration from the same run, because that one
  hasn't run yet.
- With `AllInOneTx`, a run that contains a no-transaction migration is no longer
  atomic. The transactional part is committed before the no-transaction part
  starts, and `Finish` then has nothing left to commit (it still closes the
  connection and is safe to call).
- `CREATE INDEX CONCURRENTLY` waits for every older transaction in the database
  to finish, so any long-running transaction (including one the caller holds
  open itself) delays it.

### Execution

A no-transaction migration runs as a single `Exec`, with no `BEGIN`, on a
connection of its own. Only after that succeeds is its row inserted into (or
deleted from) the migrations table, in a separate short transaction committed at
once. If the statement fails, the error is returned and nothing is recorded.

**One statement per file.** The file is sent as one query string, and Postgres
runs a multi-statement query string in an implicit transaction, so it rejects
`CONCURRENTLY` there. That rejection is the only enforcement (pqmigrate does
not parse the SQL), and a second statement next to a non-`CONCURRENTLY` one is
not caught at all, so put each such statement in its own migration.

**Write them idempotently** (`IF NOT EXISTS`, `IF EXISTS`). If the statement
succeeds but recording it fails, the error says so, and a rerun executes the
statement again.

### Failed concurrent index builds

A failed `CREATE INDEX CONCURRENTLY` leaves an **INVALID** index behind. It is
not recorded, so it will be retried, but a retry with `IF NOT EXISTS` silently
skips the invalid index and records the migration as done. After a failure,
`DROP INDEX CONCURRENTLY` the invalid index and run the migration again. Find
leftovers with `select indexrelid::regclass from pg_index where not indisvalid`.

## Migration lock

`Config.LockKey` (0 = off) serialises concurrent migrators, e.g. several pods
starting at once. `MigrateUp`, `MigrateDown`, `MigrateUpFile`, `MigrateDownFile`
and `Sync` take a session-level advisory lock with that key, on a dedicated
connection, before reading migration state. They hold it until the call's work,
including the deferred no-transaction phase, is done:

- If the call leaves a transaction open (`AllInOneTx` without no-transaction
  migrations, or an error), the lock is held until `Finish` has committed. A
  second migrator therefore sees the recorded rows. **Always call `Finish`.**
- Otherwise it is released before the call returns.

A waiting migrator polls `pg_try_advisory_lock` instead of blocking in
`pg_advisory_lock`. A session blocked inside a statement holds a snapshot, and
the lock holder's `CREATE INDEX CONCURRENTLY` would wait for that snapshot
forever. Postgres can't detect that deadlock, because the lock and the index
build are on different connections.

The lock belongs to one database session, so `DBUrl` must point at Postgres
directly or at a session-pooled pooler. Behind transaction pooling (PgBouncer
`pool_mode=transaction`) the lock calls can land on different server
connections, so it neither excludes other migrators nor reliably releases;
leave `LockKey` at 0 there. The wait has no timeout; a waiter logs every 30
seconds, and a stuck holder (find it in `pg_locks` where `locktype =
'advisory'`) has to be terminated by hand.


## License
[![FOSSA Status](https://app.fossa.io/api/projects/git%2Bgithub.com%2FPreciselyco%2Fpqmigrate.svg?type=large)](https://app.fossa.io/projects/git%2Bgithub.com%2FPreciselyco%2Fpqmigrate?ref=badge_large)
