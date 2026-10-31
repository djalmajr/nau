-- The machine-connect tables: the machines of the developers, the
-- pairing codes that connect a machine to its owner, the failed
-- confirmations of the codes and the deliveries of the inventories.
-- The ids are random text (never sequential); the time is in
-- milliseconds. The keys and the codes are stored only as SHA-256
-- hashes.

CREATE TABLE machines (
    id TEXT PRIMARY KEY,
    developer_id TEXT NOT NULL REFERENCES users (id),
    name TEXT NOT NULL,
    key_hash TEXT NOT NULL UNIQUE,
    created_ms INTEGER NOT NULL,
    revoked_ms INTEGER
);

CREATE TABLE pairings (
    id TEXT PRIMARY KEY,
    code_hash TEXT NOT NULL UNIQUE,
    poll_hash TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    client_hash TEXT NOT NULL, -- the SHA-256 of the ip of the connection that created the pairing (the empty string when it is unknown); never the raw ip
    created_ms INTEGER NOT NULL,
    expires_ms INTEGER NOT NULL,
    approved_by TEXT,
    machine_id TEXT, -- the machine born of this pairing; the atomic delivery keeps the link coherent (no foreign key: the machine insert comes after the pairing update in the same batch)
    consumed INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE pairing_attempts (
    user_id TEXT NOT NULL,
    at_ms INTEGER NOT NULL
);

CREATE TABLE ingest_deliveries (
    machine_id TEXT NOT NULL,
    at_ms INTEGER NOT NULL
);

-- The guard table of the machine delivery: the consume-once step of
-- the batch writes the machine row only when the guarded update of the
-- pairing changed exactly one row; the CHECK fails when it changed none
-- (the pairing was already consumed), the failed statement aborts the
-- batch, and the whole delivery rolls back. The delete after the check
-- keeps the table empty between deliveries.
CREATE TABLE pairing_guard (
    ok INTEGER NOT NULL CHECK (ok = 1)
);

CREATE INDEX pairing_attempts_at_idx ON pairing_attempts (at_ms);

CREATE INDEX ingest_deliveries_machine_at_idx ON ingest_deliveries (machine_id, at_ms);

CREATE INDEX pairings_client_created_idx ON pairings (client_hash, created_ms);
