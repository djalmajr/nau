package machines

import (
	"context"

	"github.com/djalmajr/nau/internal/hub"
	"github.com/djalmajr/nau/internal/hub/ingest"
	"github.com/djalmajr/nau/internal/inventory"
)

// ingestDeliveries is the POST /api/ingest route, behind
// RequireMachine: the machine delivers the inventory of its agents.
// The body is the form of the soho inventory; it is parsed without
// repeating the reason of a refusal and applied through the ingest
// layer.
func (m *Service) ingestDeliveries(ctx context.Context, req hub.Request, mc Machine) hub.Response {
	if !m.checkContentType(req) {
		return errResponse(415, "unsupported_media_type")
	}
	nowMS := m.nowMS()
	// The delivery limit is per machine, reserved in the database
	// before the operation, so it holds between instances and between
	// concurrent deliveries. A delivery always counts, applied or
	// refused; a failure of the mechanism is a 503, without
	// proceeding.
	reserved, err := m.machines.ReserveDelivery(ctx, mc.ID, nowMS)
	if err != nil {
		return unavailable()
	}
	if !reserved {
		return errResponse(429, "rate_limited")
	}
	inv, err := inventory.Parse(req.Body)
	if err != nil {
		// The reason of the refusal is not repeated to the caller.
		return errResponse(400, "bad_inventory")
	}
	res, err := m.apply(ctx, m.store, mc.ID, inv, nowMS)
	if err != nil {
		if err == ingest.ErrConflict {
			return errResponse(409, "conflict")
		}
		return unavailable()
	}
	if res.Applied {
		return hub.Response{Status: 204}
	}
	return hub.Response{Status: 202}
}
