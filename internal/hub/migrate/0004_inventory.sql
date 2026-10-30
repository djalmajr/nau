CREATE TABLE inventory_snapshots (
    machine_id TEXT PRIMARY KEY,
    generated_ms INTEGER NOT NULL,
    received_ms INTEGER NOT NULL,
    soho_version TEXT NOT NULL
);

CREATE TABLE agents_current (
    machine_id TEXT NOT NULL,
    terminal TEXT NOT NULL,
    project TEXT NOT NULL,
    session TEXT NOT NULL,
    name TEXT NOT NULL,
    role TEXT NOT NULL,
    kind TEXT NOT NULL,
    model TEXT,
    effort TEXT,
    state TEXT NOT NULL,
    reason TEXT,
    task TEXT,
    report_ready INTEGER NOT NULL,
    started_ms INTEGER NOT NULL,
    since_ms INTEGER,
    observed_ms INTEGER NOT NULL,
    PRIMARY KEY (machine_id, terminal)
);

CREATE TABLE agent_history (
    machine_id TEXT NOT NULL,
    terminal TEXT NOT NULL,
    project TEXT NOT NULL,
    name TEXT NOT NULL,
    state TEXT NOT NULL,
    reason TEXT,
    at_ms INTEGER NOT NULL
);

CREATE INDEX agent_history_machine_terminal_at ON agent_history (machine_id, terminal, at_ms);

CREATE INDEX agent_history_at ON agent_history (at_ms);

CREATE TABLE hidden_counts (
    machine_id TEXT PRIMARY KEY,
    working INTEGER NOT NULL,
    needs_you INTEGER NOT NULL,
    done INTEGER NOT NULL,
    idle INTEGER NOT NULL,
    unknown INTEGER NOT NULL,
    observed_ms INTEGER NOT NULL
);

CREATE TABLE working_hourly (
    machine_id TEXT NOT NULL,
    hour_ms INTEGER NOT NULL,
    max_working INTEGER NOT NULL,
    PRIMARY KEY (machine_id, hour_ms)
);
