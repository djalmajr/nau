CREATE TABLE schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_ms INTEGER NOT NULL
);

CREATE TABLE hub_meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
