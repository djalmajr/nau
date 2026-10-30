-- The guard table of the guarded writes of the ingest: the guarded
-- step of the batch writes the snapshot only when the version the
-- call read is still the current one, and this table records the
-- number of rows that step changed. The CHECK fails when the number
-- is not exactly 1, the failed statement aborts the batch, and the
-- whole write rolls back. The delete after the check keeps the table
-- empty between applies.
CREATE TABLE ingest_guard (
    ok INTEGER NOT NULL CHECK (ok = 1)
);
