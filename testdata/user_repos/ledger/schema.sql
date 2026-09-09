-- The ledger's own schema, as its migrations would have left it.
--
-- Here so that what Mendel is tested against is a repository's database rather
-- than a shape written next to an assertion. The experiment this repository
-- declares in .mendel/experiment.json adds a column to `entries`, so this is
-- what that migration has to be admissible against — and if the two ever
-- disagree, that is the fixture catching a spec drifting rather than a test
-- checking itself.
--
-- Deliberately not exhaustive. It carries the properties admission actually
-- turns on: a table with an identity to archive by, a table without one, and a
-- column an additive change can sit beside.

CREATE TABLE accounts (
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    opened_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE entries (
    id SERIAL PRIMARY KEY,
    account_id INT NOT NULL REFERENCES accounts(id),
    amount_cents BIGINT NOT NULL,
    memo TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX entries_by_account ON entries (account_id);

-- No primary key, on purpose. Admission refuses to run an experiment that
-- writes somewhere it could not archive from, and this is what that refusal is
-- tested against. A real ledger would have an append-only audit table shaped
-- much like it.
CREATE TABLE audit_log (
    at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    actor TEXT NOT NULL,
    detail TEXT
);

INSERT INTO accounts (name) VALUES ('operating'), ('reserve');
INSERT INTO entries (account_id, amount_cents, memo)
VALUES (1, 125000, 'opening balance'), (1, -4200, 'coffee'), (2, 500000, 'transfer');
