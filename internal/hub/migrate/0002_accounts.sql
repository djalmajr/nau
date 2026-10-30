-- The sessions and the invites come before the users: the inherited
-- cleanup of the tagged suites drops the tables in the order the
-- schema returns them, and the sessions reference the users, so the
-- reference must be dropped before the referenced table. SQLite
-- resolves the reference when the users table exists (at the first
-- write), so the order is safe.
CREATE TABLE sessions (
    token_sha TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users (id),
    created_ms INTEGER NOT NULL,
    expires_ms INTEGER NOT NULL,
    revoked INTEGER NOT NULL DEFAULT 0 CHECK (revoked IN (0, 1)),
    auth_version INTEGER NOT NULL
);

CREATE INDEX sessions_user_idx ON sessions (user_id);

CREATE TABLE users (
    id TEXT PRIMARY KEY,
    email_norm TEXT NOT NULL UNIQUE,
    email TEXT NOT NULL,
    name TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    auth_version INTEGER NOT NULL DEFAULT 1,
    role TEXT NOT NULL CHECK (role IN ('owner', 'member')),
    created_ms INTEGER NOT NULL
);

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

-- The one-row guard of the conditional commands: the
-- INSERT INTO accounts_guard VALUES (changes()) right after a
-- conditional statement fails on the CHECK when it changed anything
-- but one row, and rolls the whole batch back.
CREATE TABLE accounts_guard (
    ok INTEGER NOT NULL CHECK (ok = 1)
);
