package machines

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/djalmajr/nau/internal/hub"
)

// developerRef is one developer of the read answers.
type developerRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// machineItem is the machine of the listing: the user's own machines,
// the machines shared with the user, and the user's own revoked
// machines, with the seal.
type machineItem struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Developer  developerRef `json:"developer"`
	CreatedMS  int64        `json:"created_ms"`
	Revoked    bool         `json:"revoked"`
	LastSeenMS *int64       `json:"last_seen_ms"`
	Online     bool         `json:"online"`
	// SharedWith holds the developers the machine is shared with,
	// filled only for the user's own machines.
	SharedWith *[]developerRef `json:"shared_with,omitempty"`
}

// listMachines is the GET /api/machines route (session required): the
// non-revoked machines of the signed-in user and the non-revoked
// machines shared with them, and the user's own revoked machines, with
// the seal and the last-seen they have; the owner of the hub sees the
// same rule, not all the machines.
func (m *Service) listMachines(ctx context.Context, req hub.Request) hub.Response {
	u, ok := m.sessionUser(ctx, req)
	if !ok {
		return errResponse(401, "unauthenticated")
	}
	nowMS := m.nowMS()
	list, err := m.sharing.VisibleMachines(ctx, u.ID, nowMS)
	if err != nil {
		return unavailable()
	}
	revoked, err := m.ownRevoked(ctx, m.store, u.ID, nowMS)
	if err != nil {
		return unavailable()
	}
	items := make([]machineItem, 0, len(list)+len(revoked))
	for _, mc := range list {
		item := machineItem{
			ID:         mc.ID,
			Name:       mc.Name,
			Developer:  developerRef{ID: mc.Developer.ID, Name: mc.Developer.Name},
			CreatedMS:  mc.CreatedMS,
			Revoked:    false,
			LastSeenMS: mc.LastSeenMS,
			Online:     mc.Online,
		}
		if mc.Own {
			shared := make([]developerRef, 0, len(mc.SharedWith))
			for _, d := range mc.SharedWith {
				shared = append(shared, developerRef{ID: d.ID, Name: d.Name})
			}
			item.SharedWith = &shared
		}
		items = append(items, item)
	}
	for _, mc := range revoked {
		// The revoked machine holds no read: the shared_with stays
		// empty, as the revocation ends the grants.
		shared := []developerRef{}
		items = append(items, machineItem{
			ID:         mc.ID,
			Name:       mc.Name,
			Developer:  developerRef{ID: mc.Developer.ID, Name: mc.Developer.Name},
			CreatedMS:  mc.CreatedMS,
			Revoked:    true,
			LastSeenMS: mc.LastSeenMS,
			Online:     mc.Online,
			SharedWith: &shared,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	out, _ := json.Marshal(struct {
		Machines []machineItem `json:"machines"`
	}{Machines: items})
	return hub.Response{Status: 200, Body: out}
}

// revokeMachine is the DELETE /api/machines/{id} route (session
// required): the developer of the machine revokes it; any other user,
// the owner of the hub included, receives 404, so the existence of
// the machine is not revealed. The revocation ends the reading of the
// machine for the shared developers too: a revoked machine is not
// visible.
func (m *Service) revokeMachine(ctx context.Context, req hub.Request) hub.Response {
	u, ok := m.sessionUser(ctx, req)
	if !ok {
		return errResponse(401, "unauthenticated")
	}
	if !m.checkContentType(req) {
		return errResponse(415, "unsupported_media_type")
	}
	id := req.PathParam("id")
	mc, found, err := m.machines.FindMachineByID(ctx, id)
	if err != nil {
		return unavailable()
	}
	if !found {
		return errResponse(404, "not_found")
	}
	if mc.DeveloperID != u.ID {
		return errResponse(404, "not_found")
	}
	if _, err := m.machines.RevokeMachine(ctx, id, m.nowMS()); err != nil {
		return unavailable()
	}
	return hub.Response{Status: 204}
}
