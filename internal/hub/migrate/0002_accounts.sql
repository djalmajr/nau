CREATE TABLE users (
    id TEXT PRIMARY KEY,
    email_norm TEXT NOT NULL UNIQUE,
    email TEXT NOT NULL,
    name TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('owner', 'member')),
    created_ms INTEGER NOT NULL
);

CREATE TABLE sessions (
    token_sha TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users (id),
    created_ms INTEGER NOT NULL,
    expires_ms INTEGER NOT NULL,
    revoked INTEGER NOT NULL DEFAULT 0 CHECK (revoked IN (0, 1))
);

CREATE INDEX sessions_user_idx ON sessions (user_id);

CREATE TABLE invites (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    email_norm TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('member')),
    token_hash TEXT NOT NULL UNIQUE,
    created_ms INTEGER NOT NULL,
    expires_ms INTEGER NOT NULL,
    revoked INTEGER NOT NULL DEFAULT 0 CHECK (revoked IN (0, 1)),
    used INTEGER NOT NULL DEFAULT 0 CHECK (used IN (0, 1))
);

CREATE TABLE login_failures (
    email_norm TEXT NOT NULL,
    at_ms INTEGER NOT NULL
);

CREATE INDEX login_failures_at_idx ON login_failures (at_ms);
