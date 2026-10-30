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
// repeating the reason of a refusal, applied through the ingest layer,
// and counted for the delivery limit when accepted.
func (m *Service) ingestDeliveries(ctx context.Context, req hub.Request, mc Machine) hub.Response {
	if !m.checkContentType(req) {
		return errResponse(415, "unsupported_media_type")
	}
	nowMS := m.nowMS()
	// The delivery limit is per machine, in the database: the
	// accepted deliveries of the window. A refused delivery (the bad
	// body, the conflict) does not count.
	accepted, err := m.machines.CountDeliveries(ctx, mc.ID, nowMS-deliveryWindow.Milliseconds())
	if err != nil {
		return unavailable()
	}
	if accepted >= maxDeliveries {
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
	// The accepted deliveries count for the limit: the applied ones
	// and the old ones the call ignored.
	_ = m.machines.RecordDelivery(ctx, mc.ID, nowMS)
	if res.Applied {
		return hub.Response{Status: 204}
	}
	return hub.Response{Status: 202}
}
