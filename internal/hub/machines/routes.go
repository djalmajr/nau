package machines

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/djalmajr/nau/internal/hub"
	"github.com/djalmajr/nau/internal/hub/accounts"
)

// machineItem is the machine of the listing, without the key hash.
type machineItem struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CreatedMS  int64  `json:"created_ms"`
	Revoked    bool   `json:"revoked"`
	LastSeenMS *int64 `json:"last_seen_ms"`
}

// listMachines is the GET /api/machines route (session required): the
// machines of the signed-in user; the owner of the hub sees all of
// them.
func (m *Service) listMachines(ctx context.Context, req hub.Request) hub.Response {
	u, ok := m.sessionUser(ctx, req)
	if !ok {
		return errResponse(401, "unauthenticated")
	}
	var (
		list []Machine
		err  error
	)
	if u.Role == accounts.RoleOwner {
		list, err = m.machines.ListAllMachines(ctx)
	} else {
		list, err = m.machines.ListMachinesByDeveloper(ctx, u.ID)
	}
	if err != nil {
		return unavailable()
	}
	ids := make([]string, 0, len(list))
	for _, mc := range list {
		ids = append(ids, mc.ID)
	}
	lastSeen, err := m.machines.LastSeen(ctx, ids)
	if err != nil {
		return unavailable()
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].CreatedMS != list[j].CreatedMS {
			return list[i].CreatedMS < list[j].CreatedMS
		}
		return list[i].ID < list[j].ID
	})
	items := make([]machineItem, 0, len(list))
	for _, mc := range list {
		item := machineItem{
			ID:        mc.ID,
			Name:      mc.Name,
			CreatedMS: mc.CreatedMS,
			Revoked:   mc.RevokedMS != nil,
		}
		if seen, has := lastSeen[mc.ID]; has {
			item.LastSeenMS = &seen
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
// not revealed.
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
