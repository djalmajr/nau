package machines

import (
	"context"
	"encoding/json"

	"github.com/djalmajr/nau/internal/hub"
	"github.com/djalmajr/nau/internal/hub/accounts"
	"github.com/djalmajr/nau/internal/hub/visibility"
)

// shareRequest is the body of the sharing route.
type shareRequest struct {
	Email string `json:"email"`
}

// shareMachine is the POST /api/machines/{id}/shares route (session
// required): the developer of the machine shares it, read-only, with
// the account of the e-mail. The 404 is the same for a machine that
// is not the user's own and an e-mail with no account.
func (m *Service) shareMachine(ctx context.Context, req hub.Request) hub.Response {
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
	if !found || mc.DeveloperID != u.ID || mc.RevokedMS != nil {
		return errResponse(404, "not_found")
	}
	var body shareRequest
	if err := decodeStrict(req.Body, &body); err != nil {
		return errResponse(400, "bad_request")
	}
	if visibility.EmailNorm(body.Email) == u.EmailNorm {
		return errResponse(400, "bad_request")
	}
	acct, found, err := m.sharing.UserByEmail(ctx, body.Email)
	if err != nil {
		return unavailable()
	}
	if !found || acct.ID == u.ID {
		return errResponse(404, "not_found")
	}
	if err := m.sharing.Share(ctx, id, acct.ID, m.nowMS()); err != nil {
		return unavailable()
	}
	out, _ := json.Marshal(struct {
		Developer developerRef `json:"developer"`
	}{Developer: developerRef{ID: acct.ID, Name: acct.Name}})
	return hub.Response{Status: 201, Body: out}
}

// unshareMachine is the DELETE /api/machines/{id}/shares/
// {developerId} route (session required): the developer of the
// machine ends the grant; the 404 is the same when the grant does not
// exist and when the machine is not the user's own.
func (m *Service) unshareMachine(ctx context.Context, req hub.Request) hub.Response {
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
	if !found || mc.DeveloperID != u.ID {
		return errResponse(404, "not_found")
	}
	existed, err := m.sharing.Unshare(ctx, id, req.PathParam("developerId"))
	if err != nil {
		return unavailable()
	}
	if !existed {
		return errResponse(404, "not_found")
	}
	return hub.Response{Status: 204}
}

// listDevelopers is the GET /api/developers route (session required):
// for the member, the user, the developers that share a machine with
// the user and the developers the user shares with; the owner of the
// hub sees every account, without gaining any machine read that is
// not shared with them.
func (m *Service) listDevelopers(ctx context.Context, req hub.Request) hub.Response {
	u, ok := m.sessionUser(ctx, req)
	if !ok {
		return errResponse(401, "unauthenticated")
	}
	accounts, err := m.sharing.Accounts(ctx, u.ID, u.Role == accounts.RoleOwner)
	if err != nil {
		return unavailable()
	}
	type developerItem struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		Email        string `json:"email"`
		Machines     int    `json:"machines"`
		SharesWithMe int    `json:"shares_with_me"`
		SharedByMe   int    `json:"shared_by_me"`
	}
	items := make([]developerItem, 0, len(accounts))
	for _, a := range accounts {
		items = append(items, developerItem{
			ID:           a.ID,
			Name:         a.Name,
			Email:        a.Email,
			Machines:     a.Machines,
			SharesWithMe: a.SharesWithMe,
			SharedByMe:   a.SharedByMe,
		})
	}
	out, _ := json.Marshal(struct {
		Developers []developerItem `json:"developers"`
	}{Developers: items})
	return hub.Response{Status: 200, Body: out}
}
