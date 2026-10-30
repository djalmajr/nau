package machines

import (
	"context"
	"fmt"
	"strings"

	"github.com/djalmajr/nau/internal/hub/store"
)

// The SQL constants of the machine store.
const (
	// machineColumns is the select column list of the machines.
	machineColumns = "id, developer_id, name, key_hash, created_ms, revoked_ms"
	// pairingColumns is the select column list of the pairings.
	pairingColumns = "id, code_hash, poll_hash, name, created_ms, expires_ms, approved_by, machine_id, consumed"
	// lastSeenChunk is the chunk size of the IN list of the last-seen
	// query: far below the variable limit of the database.
	lastSeenChunk = 500
)

// SQL is the MachineStore over the hub storage: one statement per
// simple operation and one atomic batch per multi-statement operation
// (the pairing create with the retention, the confirmation, the
// delivery with its guard, the record with the retention).
type SQL struct {
	store store.Store
}

// NewSQL builds the SQL machine store over the hub storage.
func NewSQL(s store.Store) *SQL {
	return &SQL{store: s}
}

// intResult reads the single integer of one result row.
func intResult(res store.Result) (int64, error) {
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return 0, fmt.Errorf("machines: no row in the result")
	}
	v, ok := res.Rows[0][0].(int64)
	if !ok {
		return 0, fmt.Errorf("machines: the result is not an integer")
	}
	return v, nil
}

// machineRow reads one machine out of the select columns: id,
// developer_id, name, key_hash, created_ms, revoked_ms. It reports
// false with no error when there is no row, and an error when the
// shape of the row is not the expected one.
func machineRow(res store.Result) (Machine, bool, error) {
	if len(res.Rows) == 0 {
		return Machine{}, false, nil
	}
	row := res.Rows[0]
	if len(row) != 6 {
		return Machine{}, false, fmt.Errorf("machines: the machine row has %d columns, want 6", len(row))
	}
	id, ok := row[0].(string)
	if !ok {
		return Machine{}, false, fmt.Errorf("machines: the machine id is not a string")
	}
	developerID, ok := row[1].(string)
	if !ok {
		return Machine{}, false, fmt.Errorf("machines: the developer id is not a string")
	}
	name, ok := row[2].(string)
	if !ok {
		return Machine{}, false, fmt.Errorf("machines: the machine name is not a string")
	}
	keyHash, ok := row[3].(string)
	if !ok {
		return Machine{}, false, fmt.Errorf("machines: the key hash is not a string")
	}
	createdMS, ok := row[4].(int64)
	if !ok {
		return Machine{}, false, fmt.Errorf("machines: the created_ms is not an integer")
	}
	var revokedMS *int64
	if row[5] != nil {
		v, ok := row[5].(int64)
		if !ok {
			return Machine{}, false, fmt.Errorf("machines: the revoked_ms is not an integer")
		}
		revokedMS = &v
	}
	return Machine{ID: id, DeveloperID: developerID, Name: name, KeyHash: keyHash, CreatedMS: createdMS, RevokedMS: revokedMS}, true, nil
}

// pairingRow reads one pairing out of the select columns: id,
// code_hash, poll_hash, name, created_ms, expires_ms, approved_by,
// machine_id, consumed. It reports false with no error when there is
// no row, and an error when the shape of the row is not the expected
// one.
func pairingRow(res store.Result) (Pairing, bool, error) {
	if len(res.Rows) == 0 {
		return Pairing{}, false, nil
	}
	row := res.Rows[0]
	if len(row) != 9 {
		return Pairing{}, false, fmt.Errorf("machines: the pairing row has %d columns, want 9", len(row))
	}
	id, ok := row[0].(string)
	if !ok {
		return Pairing{}, false, fmt.Errorf("machines: the pairing id is not a string")
	}
	codeHash, ok := row[1].(string)
	if !ok {
		return Pairing{}, false, fmt.Errorf("machines: the code hash is not a string")
	}
	pollHash, ok := row[2].(string)
	if !ok {
		return Pairing{}, false, fmt.Errorf("machines: the poll hash is not a string")
	}
	name, ok := row[3].(string)
	if !ok {
		return Pairing{}, false, fmt.Errorf("machines: the pairing name is not a string")
	}
	createdMS, ok := row[4].(int64)
	if !ok {
		return Pairing{}, false, fmt.Errorf("machines: the created_ms is not an integer")
	}
	expiresMS, ok := row[5].(int64)
	if !ok {
		return Pairing{}, false, fmt.Errorf("machines: the expires_ms is not an integer")
	}
	var approvedBy *string
	if row[6] != nil {
		v, ok := row[6].(string)
		if !ok {
			return Pairing{}, false, fmt.Errorf("machines: the approved_by is not a string")
		}
		approvedBy = &v
	}
	var machineID *string
	if row[7] != nil {
		v, ok := row[7].(string)
		if !ok {
			return Pairing{}, false, fmt.Errorf("machines: the machine_id is not a string")
		}
		machineID = &v
	}
	consumed, ok := row[8].(int64)
	if !ok {
		return Pairing{}, false, fmt.Errorf("machines: the consumed flag is not an integer")
	}
	return Pairing{
		ID:         id,
		CodeHash:   codeHash,
		PollHash:   pollHash,
		Name:       name,
		CreatedMS:  createdMS,
		ExpiresMS:  expiresMS,
		ApprovedBy: approvedBy,
		MachineID:  machineID,
		Consumed:   consumed == 1,
	}, true, nil
}

func (s *SQL) CreatePairing(ctx context.Context, p Pairing, nowMS int64) error {
	_, err := s.store.Batch(ctx, []store.Stmt{
		// The retention: the pairings expired beyond the validity and
		// the attempts outside the window.
		{SQL: `DELETE FROM pairings WHERE expires_ms < ?`, Args: []store.Value{nowMS - pairingTTL.Milliseconds()}},
		{SQL: `DELETE FROM pairing_attempts WHERE at_ms <= ?`, Args: []store.Value{nowMS - confirmWindow.Milliseconds()}},
		{SQL: `INSERT INTO pairings (id, code_hash, poll_hash, name, created_ms, expires_ms, approved_by, machine_id, consumed) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)`,
			Args: []store.Value{p.ID, p.CodeHash, p.PollHash, p.Name, p.CreatedMS, p.ExpiresMS, nullable(p.ApprovedBy), nullable(p.MachineID)}},
	})
	return err
}

// nullable converts the pointer to the argument the driver
// understands: the SQL nil when the pointer is nil, the string
// otherwise.
func nullable(v *string) store.Value {
	if v == nil {
		return nil
	}
	return *v
}

func (s *SQL) PendingPairings(ctx context.Context) (int, error) {
	res, err := s.store.Execute(ctx, store.Stmt{SQL: `SELECT COUNT(*) FROM pairings WHERE consumed = 0`})
	if err != nil {
		return 0, err
	}
	n, err := intResult(res)
	return int(n), err
}

func (s *SQL) PairingsCreatedSince(ctx context.Context, sinceMS int64) (int, error) {
	res, err := s.store.Execute(ctx, store.Stmt{
		SQL:  `SELECT COUNT(*) FROM pairings WHERE created_ms > ?`,
		Args: []store.Value{sinceMS},
	})
	if err != nil {
		return 0, err
	}
	n, err := intResult(res)
	return int(n), err
}

func (s *SQL) FindPairingByCodeHash(ctx context.Context, codeHash string) (Pairing, bool, error) {
	res, err := s.store.Execute(ctx, store.Stmt{
		SQL:  `SELECT ` + pairingColumns + ` FROM pairings WHERE code_hash = ?`,
		Args: []store.Value{codeHash},
	})
	r, found, rowErr := pairingRow(res)
	return r, found, firstErr(err, rowErr)
}

func (s *SQL) FindPairingByPollHash(ctx context.Context, pollHash string) (Pairing, bool, error) {
	res, err := s.store.Execute(ctx, store.Stmt{
		SQL:  `SELECT ` + pairingColumns + ` FROM pairings WHERE poll_hash = ?`,
		Args: []store.Value{pollHash},
	})
	r, found, rowErr := pairingRow(res)
	return r, found, firstErr(err, rowErr)
}

// FindPairingByID finds one pairing by its id (the re-read of the
// delivery disambiguation).
func (s *SQL) FindPairingByID(ctx context.Context, id string) (Pairing, bool, error) {
	res, err := s.store.Execute(ctx, store.Stmt{
		SQL:  `SELECT ` + pairingColumns + ` FROM pairings WHERE id = ?`,
		Args: []store.Value{id},
	})
	r, found, rowErr := pairingRow(res)
	return r, found, firstErr(err, rowErr)
}

// ConfirmPairing links the pairing to the user when it is not
// confirmed yet; the single guarded statement is the whole operation.
func (s *SQL) ConfirmPairing(ctx context.Context, pairingID, userID string) (bool, error) {
	res, err := s.store.Execute(ctx, store.Stmt{
		SQL:  `UPDATE pairings SET approved_by = ? WHERE id = ? AND approved_by IS NULL`,
		Args: []store.Value{userID, pairingID},
	})
	if err != nil {
		return false, err
	}
	return res.Affected == 1, nil
}

// RecordPairingError stores one wrong confirmation and prunes the
// attempts outside the window, in one atomic batch, so the table
// stays bounded.
func (s *SQL) RecordPairingError(ctx context.Context, userID string, atMS int64) error {
	_, err := s.store.Batch(ctx, []store.Stmt{
		{SQL: `INSERT INTO pairing_attempts (user_id, at_ms) VALUES (?, ?)`, Args: []store.Value{userID, atMS}},
		{SQL: `DELETE FROM pairing_attempts WHERE at_ms <= ?`, Args: []store.Value{atMS - confirmWindow.Milliseconds()}},
	})
	return err
}

func (s *SQL) CountPairingErrors(ctx context.Context, userID string, sinceMS int64) (int, error) {
	res, err := s.store.Execute(ctx, store.Stmt{
		SQL:  `SELECT COUNT(*) FROM pairing_attempts WHERE user_id = ? AND at_ms > ?`,
		Args: []store.Value{userID, sinceMS},
	})
	if err != nil {
		return 0, err
	}
	n, err := intResult(res)
	return int(n), err
}

// DeliverMachine consumes the pairing (exactly once) and stores the
// machine in one atomic batch, guarded: the guard check fails the
// batch when the consume-once update changed no row (the pairing was
// already consumed), and the whole delivery rolls back. A failed batch
// is disambiguated by re-reading the pairing: consumed means the call
// lost the consume-once race (false, no error), anything else is the
// store failure.
func (s *SQL) DeliverMachine(ctx context.Context, pairingID string, m Machine) (bool, error) {
	_, err := s.store.Batch(ctx, []store.Stmt{
		{SQL: `UPDATE pairings SET consumed = 1, machine_id = ? WHERE id = ? AND consumed = 0 AND approved_by IS NOT NULL`,
			Args: []store.Value{m.ID, pairingID}},
		// The guard fails the batch when the update changed no row:
		// the CHECK of the table rejects anything but 1.
		{SQL: `INSERT INTO pairing_guard VALUES (changes())`},
		// The delete after the check keeps the table empty between
		// deliveries.
		{SQL: `DELETE FROM pairing_guard`},
		{SQL: `INSERT INTO machines (id, developer_id, name, key_hash, created_ms, revoked_ms) VALUES (?, ?, ?, ?, ?, NULL)`,
			Args: []store.Value{m.ID, m.DeveloperID, m.Name, m.KeyHash, m.CreatedMS}},
	})
	if err == nil {
		// A success is a delivery: the guard only passes when the
		// consume-once update changed exactly one row.
		return true, nil
	}
	p, found, rerr := s.FindPairingByID(ctx, pairingID)
	if rerr == nil && found && p.Consumed {
		// The pairing was consumed in the window of the call: the
		// guard failed, nothing was applied, and the answer is the
		// already-consumed one.
		return false, nil
	}
	return false, err
}

func (s *SQL) FindMachineByKeyHash(ctx context.Context, keyHash string) (Machine, bool, error) {
	res, err := s.store.Execute(ctx, store.Stmt{
		SQL:  `SELECT ` + machineColumns + ` FROM machines WHERE key_hash = ?`,
		Args: []store.Value{keyHash},
	})
	r, found, rowErr := machineRow(res)
	return r, found, firstErr(err, rowErr)
}

func (s *SQL) FindMachineByID(ctx context.Context, id string) (Machine, bool, error) {
	res, err := s.store.Execute(ctx, store.Stmt{
		SQL:  `SELECT ` + machineColumns + ` FROM machines WHERE id = ?`,
		Args: []store.Value{id},
	})
	r, found, rowErr := machineRow(res)
	return r, found, firstErr(err, rowErr)
}

func (s *SQL) ListMachinesByDeveloper(ctx context.Context, developerID string) ([]Machine, error) {
	res, err := s.store.Execute(ctx, store.Stmt{
		SQL:  `SELECT ` + machineColumns + ` FROM machines WHERE developer_id = ?`,
		Args: []store.Value{developerID},
	})
	if err != nil {
		return nil, err
	}
	return machinesOf(res)
}

func (s *SQL) ListAllMachines(ctx context.Context) ([]Machine, error) {
	res, err := s.store.Execute(ctx, store.Stmt{SQL: `SELECT ` + machineColumns + ` FROM machines`})
	if err != nil {
		return nil, err
	}
	return machinesOf(res)
}

func machinesOf(res store.Result) ([]Machine, error) {
	out := make([]Machine, 0, len(res.Rows))
	for _, row := range res.Rows {
		mc, _, err := machineRow(store.Result{Rows: []store.Row{row}})
		if err != nil {
			return nil, err
		}
		out = append(out, mc)
	}
	return out, nil
}

// RevokeMachine revokes the machine when it is not revoked yet; the
// single guarded statement is the whole operation.
func (s *SQL) RevokeMachine(ctx context.Context, id string, atMS int64) (bool, error) {
	res, err := s.store.Execute(ctx, store.Stmt{
		SQL:  `UPDATE machines SET revoked_ms = ? WHERE id = ? AND revoked_ms IS NULL`,
		Args: []store.Value{atMS, id},
	})
	if err != nil {
		return false, err
	}
	return res.Affected == 1, nil
}

// LastSeen returns, for each of the machine ids that holds at least
// one inventory snapshot, the received_ms of its last inventory: the
// MAX of the received_ms of the snapshots of the machine.
func (s *SQL) LastSeen(ctx context.Context, machineIDs []string) (map[string]int64, error) {
	out := make(map[string]int64, len(machineIDs))
	for start := 0; start < len(machineIDs); start += lastSeenChunk {
		end := start + lastSeenChunk
		if end > len(machineIDs) {
			end = len(machineIDs)
		}
		ids := machineIDs[start:end]
		args := make([]store.Value, 0, len(ids)+1)
		for _, id := range ids {
			args = append(args, id)
		}
		sql := `SELECT machine_id, MAX(received_ms) FROM inventory_snapshots WHERE machine_id IN (` + inPlaceholders(len(ids)) + `) GROUP BY machine_id`
		res, err := s.store.Execute(ctx, store.Stmt{SQL: sql, Args: args})
		if err != nil {
			return nil, err
		}
		for _, row := range res.Rows {
			if len(row) != 2 {
				return nil, fmt.Errorf("machines: the last-seen row has %d columns, want 2", len(row))
			}
			id, ok := row[0].(string)
			if !ok {
				return nil, fmt.Errorf("machines: the last-seen machine id is not a string")
			}
			ms, ok := row[1].(int64)
			if !ok {
				return nil, fmt.Errorf("machines: the last-seen instant is not an integer")
			}
			out[id] = ms
		}
	}
	return out, nil
}

// inPlaceholders is the placeholder list of an IN clause.
func inPlaceholders(n int) string {
	return strings.Repeat("?,", n-1) + "?"
}

// RecordDelivery stores one accepted delivery and prunes the rows
// outside the window, in one atomic batch, so the table stays
// bounded.
func (s *SQL) RecordDelivery(ctx context.Context, machineID string, atMS int64) error {
	_, err := s.store.Batch(ctx, []store.Stmt{
		{SQL: `INSERT INTO ingest_deliveries (machine_id, at_ms) VALUES (?, ?)`, Args: []store.Value{machineID, atMS}},
		{SQL: `DELETE FROM ingest_deliveries WHERE at_ms <= ?`, Args: []store.Value{atMS - deliveryWindow.Milliseconds()}},
	})
	return err
}

func (s *SQL) CountDeliveries(ctx context.Context, machineID string, sinceMS int64) (int, error) {
	res, err := s.store.Execute(ctx, store.Stmt{
		SQL:  `SELECT COUNT(*) FROM ingest_deliveries WHERE machine_id = ? AND at_ms > ?`,
		Args: []store.Value{machineID, sinceMS},
	})
	if err != nil {
		return 0, err
	}
	n, err := intResult(res)
	return int(n), err
}

// firstErr returns the first non-nil error.
func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
