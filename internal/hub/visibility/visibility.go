// Package visibility computes, for a signed-in developer, what the
// read routes may show: the machines the developer reads, the
// accounts the developers route lists and the grants of the machine
// sharing.
//
// A developer reads the non-revoked machines that are theirs or
// shared with them; the owner of the hub follows the same rule for
// the machine data, and sees every account in the developers route
// without gaining any machine read that is not shared with them.
// Every computation runs over the hub store with SQL of arguments
// only and the IN lists in parts, as in the ingest.
package visibility

import (
	"context"
	"strings"

	"github.com/djalmajr/nau/internal/hub/store"
)

// onlineWindowMS is the online window of the machine data: a machine
// whose last inventory was received within the last minute is online.
// It mirrors the online window of the ingest queries.
const onlineWindowMS = 60 * 1000

// maxStatementArgs is the bound of the arguments of one statement;
// the IN lists run in parts of that size, as in the ingest.
const maxStatementArgs = 500

// Developer is one developer, as the read answers carry it.
type Developer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Machine is one machine the developer may read, with the data of the
// read routes. Own is true when the developer is the machine's
// developer; SharedWith holds the developers the machine is shared
// with, filled only for the own machines.
type Machine struct {
	ID         string
	Name       string
	Developer  Developer
	CreatedMS  int64
	LastSeenMS *int64
	Online     bool
	Own        bool
	// Revoked is the seal of the listing: true for the own revoked
	// machines the machine listing route lists, as the separate
	// query of the route. The rule of VisibleMachines never returns
	// a revoked machine.
	Revoked    bool
	SharedWith []Developer
}

// Account is one entry of the developers answer.
type Account struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Email        string `json:"email"`
	Machines     int    `json:"machines"`
	SharesWithMe int    `json:"shares_with_me"`
	SharedByMe   int    `json:"shared_by_me"`
}

// Sharing is the state and the computation of the sharing: the
// visible machines, the grants and the accounts of the read routes.
type Sharing interface {
	// VisibleMachines returns the machines the developer may read,
	// ordered by id: the non-revoked own ones and the non-revoked
	// ones shared with them.
	VisibleMachines(ctx context.Context, developerID string, nowMS int64) ([]Machine, error)
	// UserByEmail finds the account of the e-mail, or false.
	UserByEmail(ctx context.Context, email string) (Account, bool, error)
	// Share grants the machine to the developer, idempotently.
	Share(ctx context.Context, machineID, granteeID string, nowMS int64) error
	// Unshare removes the grant: true when a share existed.
	Unshare(ctx context.Context, machineID, granteeID string) (bool, error)
	// Accounts lists the accounts of the developers route: the
	// viewer, the developers that share with the viewer and the
	// developers the viewer shares with — or every account when the
	// viewer is the owner of the hub.
	Accounts(ctx context.Context, viewerID string, owner bool) ([]Account, error)
}

// sqlSharing is the Sharing over the hub store.
type sqlSharing struct {
	store store.Store
}

// New builds the Sharing over the hub store.
func New(st store.Store) Sharing {
	return &sqlSharing{store: st}
}

// EmailNorm is the normalized e-mail of the grantee lookup, as the
// accounts normalize it: lowercase and trimmed.
func EmailNorm(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
