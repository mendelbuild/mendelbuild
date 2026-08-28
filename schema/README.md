# Schema

- `full.sql` - Complete current schema (reference only, not executed)
- `migrations/` - Incremental migration files

## Migrations

Run all pending migrations:
```
mendel migrate
```

Revert the last N migrations:
```
mendel migrate -down N
```

## Rules

1. **Never edit existing migrations** - create new ones instead
2. Keep `full.sql` updated to reflect the final schema state
3. Every `.up.sql` needs a corresponding `.down.sql`
4. **Sequence numbers must be unique** - migrations apply in lexicographic
   filename order, so a duplicate number leaves the order between the colliding
   pair up to the rest of the filename. Use `max + 1`; `go test ./schema/...`
   enforces it.
