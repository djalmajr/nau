-- 0006_shares.sql
--
-- The machine sharing of DJA-624: a share grants the read of one
-- machine to another developer. The row is the state of the grant:
-- deleting it or revoking the machine ends the read (a revoked
-- machine is not visible, so a leftover row is inert). The machine
-- and the grantee are not referenced: the sharing routes check the
-- machine's developer, and the deletes of the users and the machines
-- are not the operation of this table, as in the inventory tables.

CREATE TABLE shares (
    machine_id TEXT NOT NULL,
    grantee_id TEXT NOT NULL,
    created_ms INTEGER NOT NULL,
    PRIMARY KEY (machine_id, grantee_id)
);
