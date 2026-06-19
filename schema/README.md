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
