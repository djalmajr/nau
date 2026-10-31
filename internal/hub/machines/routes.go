package machines

import (
	"context"
	"encoding/json"

	"github.com/djalmajr/nau/internal/hub"
	"github.com/djalmajr/nau/internal/hub/accounts"
)

// developerRef is one developer of the read answers.
type developerRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// machineItem is the machine of the listing: the user's own machines
// and the machines shared with the user, never a revoked one.
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
// machines shared with them; the owner of the hub sees the same rule,
// not all the machines.
func (m *Service) listMachines(ctx context.Context, req hub.Request) hub.Response {
	u, ok := m.sessionUser(ctx, req)
	if !ok {
		return errResponse(401, "unauthenticated")
	}
	list, err := m.sharing.VisibleMachines(ctx, u.ID, m.nowMS())
	if err != nil {
		return unavailable()
	}
	items := make([]machineItem, 0, len(list))
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
	out, _ := json.Marshal(struct {
		Machines []machineItem `json:"machines"`
	}{Machines: items})
	return hub.Response{Status: 200, Body: out}
}

// revokeMachine is the DELETE /api/machines/{id} route (session
// required): the owner of the machine or the owner of the hub revokes
// it; any other user receives 404, so the existence of the machine is
// not revealed. The revocation ends the reading of the machine for
// the shared developers too: a revoked machine is not visible.
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
	if mc.DeveloperID != u.ID && u.Role != accounts.RoleOwner {
		return errResponse(404, "not_found")
	}
	if _, err := m.machines.RevokeMachine(ctx, id, m.nowMS()); err != nil {
		return unavailable()
	}
	return hub.Response{Status: 204}
}
