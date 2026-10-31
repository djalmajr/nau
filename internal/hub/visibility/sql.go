package visibility

import (
	"context"
	"sort"
	"strings"

	"github.com/djalmajr/nau/internal/hub/store"
)

// The statements of the sharing. They are controlled text: the tests
// run them over the store of the suite.
const (
	// sqlVisibleMachines lists the machine ids the developer may
	// read: the non-revoked own ones and the non-revoked ones shared
	// with them.
	sqlVisibleMachines = `SELECT m.id FROM machines m WHERE m.revoked_ms IS NULL AND (m.developer_id = ? OR EXISTS (SELECT 1 FROM shares s WHERE s.machine_id = m.id AND s.grantee_id = ?)) ORDER BY m.id`
	// sqlMachineMeta reads the machine data of the listing.
	sqlMachineMeta = `SELECT id, name, developer_id, created_ms FROM machines WHERE id IN (`
	// sqlDevelopers reads the names of the developers.
	sqlDevelopers = `SELECT id, name FROM users WHERE id IN (`
	// sqlMachineShares reads the grants of the machines.
	sqlMachineShares = `SELECT machine_id, grantee_id FROM shares WHERE machine_id IN (`
	// sqlLastSeen reads the received_ms of the last inventory of the
	// machines.
	sqlLastSeen = `SELECT machine_id, MAX(received_ms) FROM inventory_snapshots WHERE machine_id IN (`
	// sqlShare grants the machine to the developer, idempotently.
	sqlShare = `INSERT OR IGNORE INTO shares (machine_id, grantee_id, created_ms) VALUES (?, ?, ?)`
	// sqlHasShare checks the grant.
	sqlHasShare = `SELECT 1 FROM shares WHERE machine_id = ? AND grantee_id = ?`
	// sqlUnshare removes the grant.
	sqlUnshare = `DELETE FROM shares WHERE machine_id = ? AND grantee_id = ?`
	// sqlUserByEmail finds the account of the e-mail.
	sqlUserByEmail = `SELECT id, name, email FROM users WHERE email_norm = ?`
	// sqlAccountIDs lists the account ids of the developers route of
	// a member: the viewer, the developers that share a machine with
	// the viewer and the developers the viewer shares with, through
	// non-revoked machines only (the sharing of a revoked machine
	// creates no relationship).
	sqlAccountIDs = `SELECT DISTINCT u.id FROM users u WHERE u.id = ? OR u.id IN (SELECT m.developer_id FROM machines m JOIN shares s ON s.machine_id = m.id WHERE s.grantee_id = ? AND m.revoked_ms IS NULL) OR u.id IN (SELECT s.grantee_id FROM shares s JOIN machines m ON m.id = s.machine_id WHERE m.developer_id = ? AND m.revoked_ms IS NULL) ORDER BY u.id`
	// sqlAllAccountIDs lists the account ids of the developers route
	// of the owner of the hub: every account.
	sqlAllAccountIDs = `SELECT id FROM users ORDER BY id`
	// sqlAccountMeta reads the names and the e-mails of the accounts.
	sqlAccountMeta = `SELECT id, name, email FROM users WHERE id IN (`
	// sqlAccountMachines counts the non-revoked machines of the
	// accounts.
	sqlAccountMachines = `SELECT developer_id, COUNT(*) FROM machines WHERE revoked_ms IS NULL AND developer_id IN (`
	// sqlAccountSharesWithMe counts the shares that reach the
	// accounts, through non-revoked machines only (the sharing of a
	// revoked machine does not count).
	sqlAccountSharesWithMe = `SELECT s.grantee_id, COUNT(*) FROM shares s JOIN machines m ON m.id = s.machine_id WHERE m.revoked_ms IS NULL AND s.grantee_id IN (`
	// sqlAccountSharedByMe counts the shares the accounts give
	// through their non-revoked machines (the sharing of a revoked
	// machine does not count).
	sqlAccountSharedByMe = `SELECT m.developer_id, COUNT(*) FROM machines m JOIN shares s ON s.machine_id = m.id WHERE m.revoked_ms IS NULL AND m.developer_id IN (`
	// sqlOwnRevokedMachines lists the ids of the developer's own
	// revoked machines, in the id order. It is the separate query of
	// the machine listing route: the rule of VisibleMachines is
	// unchanged, and the revoked machines grant nothing to anyone.
	sqlOwnRevokedMachines = `SELECT id FROM machines WHERE developer_id = ? AND revoked_ms IS NOT NULL ORDER BY id`
)

// VisibleMachines is the rule of the decision: the non-revoked
// machines of the developer and the non-revoked machines shared with
// the developer, ordered by id. An empty list returns the empty
// answer without the metadata statements.
func (s *sqlSharing) VisibleMachines(ctx context.Context, developerID string, nowMS int64) ([]Machine, error) {
	res, err := s.store.Execute(ctx, store.Stmt{
		SQL:  sqlVisibleMachines,
		Args: []store.Value{developerID, developerID},
	})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(res.Rows))
	for _, row := range res.Rows {
		ids = append(ids, strOf(row[0]))
	}
	if len(ids) == 0 {
		return []Machine{}, nil
	}
	res, err = s.runIn(ctx, sqlMachineMeta, "", ids)
	if err != nil {
		return nil, err
	}
	type meta struct {
		id          string
		name        string
		developerID string
		createdMS   int64
	}
	byID := make(map[string]meta, len(ids))
	ownIDs := make([]string, 0, len(ids))
	developerIDs := make(map[string]bool)
	for _, row := range res.Rows {
		m := meta{id: strOf(row[0]), name: strOf(row[1]), developerID: strOf(row[2]), createdMS: int64Of(row[3])}
		byID[m.id] = m
		developerIDs[m.developerID] = true
		if m.developerID == developerID {
			ownIDs = append(ownIDs, m.id)
		}
	}
	// The grants of the own machines and the names of the developers
	// involved (the machine's developers and the grantees), in parts.
	grants := make(map[string][]string)
	if len(ownIDs) > 0 {
		res, err = s.runIn(ctx, sqlMachineShares, "", ownIDs)
		if err != nil {
			return nil, err
		}
		for _, row := range res.Rows {
			mid, grantee := strOf(row[0]), strOf(row[1])
			grants[mid] = append(grants[mid], grantee)
			developerIDs[grantee] = true
		}
	}
	names := make(map[string]string)
	if len(developerIDs) > 0 {
		devIDs := make([]string, 0, len(developerIDs))
		for id := range developerIDs {
			devIDs = append(devIDs, id)
		}
		res, err = s.runIn(ctx, sqlDevelopers, "", devIDs)
		if err != nil {
			return nil, err
		}
		for _, row := range res.Rows {
			names[strOf(row[0])] = strOf(row[1])
		}
	}
	res, err = s.runIn(ctx, sqlLastSeen, "", ids)
	if err != nil {
		return nil, err
	}
	lastSeen := make(map[string]int64, len(res.Rows))
	for _, row := range res.Rows {
		lastSeen[strOf(row[0])] = int64Of(row[1])
	}
	out := make([]Machine, 0, len(ids))
	for _, id := range ids {
		m := byID[id]
		mc := Machine{
			ID:         m.id,
			Name:       m.name,
			Developer:  Developer{ID: m.developerID, Name: names[m.developerID]},
			CreatedMS:  m.createdMS,
			SharedWith: nil,
			LastSeenMS: nil,
		}
		if m.developerID == developerID {
			mc.Own = true
			mc.SharedWith = []Developer{}
			for _, grantee := range grants[m.id] {
				mc.SharedWith = append(mc.SharedWith, Developer{ID: grantee, Name: names[grantee]})
			}
			sort.Slice(mc.SharedWith, func(i, j int) bool {
				return mc.SharedWith[i].ID < mc.SharedWith[j].ID
			})
		}
		if seen, has := lastSeen[id]; has {
			mc.LastSeenMS = &seen
			mc.Online = nowMS-seen <= onlineWindowMS
		}
		out = append(out, mc)
	}
	return out, nil
}

// ownRevokedMachines is the separate query of the machine listing
// route: the revoked machines of the developer, with the last-seen
// they have, in the id order. They hold no read: the SharedWith stays
// empty, as the revocation ends the grants, and the rule of
// VisibleMachines is unchanged.
func (s *sqlSharing) ownRevokedMachines(ctx context.Context, developerID string, nowMS int64) ([]Machine, error) {
	res, err := s.store.Execute(ctx, store.Stmt{
		SQL:  sqlOwnRevokedMachines,
		Args: []store.Value{developerID},
	})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(res.Rows))
	for _, row := range res.Rows {
		ids = append(ids, strOf(row[0]))
	}
	if len(ids) == 0 {
		return []Machine{}, nil
	}
	res, err = s.runIn(ctx, sqlMachineMeta, "", ids)
	if err != nil {
		return nil, err
	}
	type meta struct {
		id        string
		name      string
		createdMS int64
	}
	byID := make(map[string]meta, len(ids))
	for _, row := range res.Rows {
		byID[strOf(row[0])] = meta{id: strOf(row[0]), name: strOf(row[1]), createdMS: int64Of(row[3])}
	}
	res, err = s.runIn(ctx, sqlDevelopers, "", []string{developerID})
	if err != nil {
		return nil, err
	}
	name := ""
	for _, row := range res.Rows {
		name = strOf(row[1])
	}
	res, err = s.runIn(ctx, sqlLastSeen, "", ids)
	if err != nil {
		return nil, err
	}
	lastSeen := make(map[string]int64, len(res.Rows))
	for _, row := range res.Rows {
		lastSeen[strOf(row[0])] = int64Of(row[1])
	}
	out := make([]Machine, 0, len(ids))
	for _, id := range ids {
		m := byID[id]
		mc := Machine{
			ID:         id,
			Name:       m.name,
			Developer:  Developer{ID: developerID, Name: name},
			CreatedMS:  m.createdMS,
			Own:        true,
			Revoked:    true,
			SharedWith: []Developer{},
		}
		if seen, has := lastSeen[id]; has {
			mc.LastSeenMS = &seen
			mc.Online = nowMS-seen <= onlineWindowMS
		}
		out = append(out, mc)
	}
	return out, nil
}

// OwnRevokedMachines is the separate query of the machine listing
// route, over the hub store: the revoked machines of the developer,
// with the last-seen they have, in the id order. The rule of
// VisibleMachines is unchanged.
func OwnRevokedMachines(ctx context.Context, st store.Store, developerID string, nowMS int64) ([]Machine, error) {
	return (&sqlSharing{store: st}).ownRevokedMachines(ctx, developerID, nowMS)
}

// UserByEmail is the grantee lookup of the sharing route: the account
// of the e-mail, or false when no account has it.
func (s *sqlSharing) UserByEmail(ctx context.Context, email string) (Account, bool, error) {
	res, err := s.store.Execute(ctx, store.Stmt{
		SQL:  sqlUserByEmail,
		Args: []store.Value{EmailNorm(email)},
	})
	if err != nil {
		return Account{}, false, err
	}
	if len(res.Rows) == 0 {
		return Account{}, false, nil
	}
	row := res.Rows[0]
	return Account{ID: strOf(row[0]), Name: strOf(row[1]), Email: strOf(row[2])}, true, nil
}

// Share is the idempotent grant: the row of the grant enters or
// stays, and the route answers the same 201 in both.
func (s *sqlSharing) Share(ctx context.Context, machineID, granteeID string, nowMS int64) error {
	_, err := s.store.Execute(ctx, store.Stmt{
		SQL:  sqlShare,
		Args: []store.Value{machineID, granteeID, nowMS},
	})
	return err
}

// Unshare removes the grant and reports whether it existed.
func (s *sqlSharing) Unshare(ctx context.Context, machineID, granteeID string) (bool, error) {
	args := []store.Value{machineID, granteeID}
	res, err := s.store.Execute(ctx, store.Stmt{SQL: sqlHasShare, Args: args})
	if err != nil {
		return false, err
	}
	if len(res.Rows) == 0 {
		return false, nil
	}
	if _, err := s.store.Execute(ctx, store.Stmt{SQL: sqlUnshare, Args: args}); err != nil {
		return false, err
	}
	return true, nil
}

// Accounts is the data of the developers route: for the member, the
// viewer, the developers that share with the viewer and the
// developers the viewer shares with; for the owner of the hub, every
// account, with the same counts.
func (s *sqlSharing) Accounts(ctx context.Context, viewerID string, owner bool) ([]Account, error) {
	var res store.Result
	var err error
	if owner {
		res, err = s.store.Execute(ctx, store.Stmt{SQL: sqlAllAccountIDs})
	} else {
		res, err = s.store.Execute(ctx, store.Stmt{
			SQL:  sqlAccountIDs,
			Args: []store.Value{viewerID, viewerID, viewerID},
		})
	}
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(res.Rows))
	for _, row := range res.Rows {
		ids = append(ids, strOf(row[0]))
	}
	if len(ids) == 0 {
		return []Account{}, nil
	}
	res, err = s.runIn(ctx, sqlAccountMeta, "", ids)
	if err != nil {
		return nil, err
	}
	out := make([]Account, 0, len(res.Rows))
	for _, row := range res.Rows {
		out = append(out, Account{ID: strOf(row[0]), Name: strOf(row[1]), Email: strOf(row[2])})
	}
	counts := func(stmt, groupBy string, index int) (map[string]int, error) {
		res, err := s.runIn(ctx, stmt, groupBy, ids)
		if err != nil {
			return nil, err
		}
		by := make(map[string]int, len(res.Rows))
		for _, row := range res.Rows {
			by[strOf(row[index])] = int(int64Of(row[1]))
		}
		return by, nil
	}
	// The grouping by the key of the account goes after the closing
	// of the IN, so each account holds the count of its own rows.
	machines, err := counts(sqlAccountMachines, " GROUP BY developer_id", 0)
	if err != nil {
		return nil, err
	}
	withMe, err := counts(sqlAccountSharesWithMe, " GROUP BY s.grantee_id", 0)
	if err != nil {
		return nil, err
	}
	byMe, err := counts(sqlAccountSharedByMe, " GROUP BY m.developer_id", 0)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Machines = machines[out[i].ID]
		out[i].SharesWithMe = withMe[out[i].ID]
		out[i].SharedByMe = byMe[out[i].ID]
	}
	return out, nil
}

// ---- the IN parts, as in the ingest ---------------------------------

// runIn runs the IN statement in parts of the id list and joins the
// rows: one statement per part, the ids of the part staying at most
// maxStatementArgs. The ids are deduplicated and sorted, so each id
// falls in exactly one part.
func (s *sqlSharing) runIn(ctx context.Context, prefix, suffix string, ids []string) (store.Result, error) {
	var rows []store.Row
	for _, part := range inParts(ids) {
		args := make([]store.Value, 0, len(part))
		for _, id := range part {
			args = append(args, id)
		}
		res, err := s.store.Execute(ctx, store.Stmt{
			SQL:  prefix + placeholders(len(part)) + suffix,
			Args: args,
		})
		if err != nil {
			return store.Result{}, err
		}
		rows = append(rows, res.Rows...)
	}
	return store.Result{Rows: rows}, nil
}

// inParts deduplicates and sorts the ids and splits them into parts
// of at most maxStatementArgs ids.
func inParts(ids []string) [][]string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	var parts [][]string
	for len(out) > 0 {
		n := maxStatementArgs
		if len(out) < n {
			n = len(out)
		}
		parts = append(parts, out[:n])
		out = out[n:]
	}
	return parts
}

// placeholders is the IN list of n arguments.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ") + ")"
}

// strOf is the string of a store value, the empty string for the
// others.
func strOf(v store.Value) string {
	s, _ := v.(string)
	return s
}

// int64Of is the int64 of a store value, zero for the others.
func int64Of(v store.Value) int64 {
	n, _ := v.(int64)
	return n
}
