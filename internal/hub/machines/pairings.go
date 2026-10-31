package machines

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"unicode/utf8"

	"github.com/djalmajr/nau/internal/hub"
)

// createPairing is the POST /api/pairings route (no session): the
// machine requests a pairing and receives the code and the poll
// token.
func (m *Service) createPairing(ctx context.Context, req hub.Request) hub.Response {
	if !m.checkContentType(req) {
		return errResponse(415, "unsupported_media_type")
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeStrict(req.Body, &body); err != nil {
		return badRequest()
	}
	name := body.Name
	runeCount := utf8.RuneCountInString(name)
	if runeCount < 1 || runeCount > 64 || hasControlRune(name) {
		return badRequest()
	}
	nowMS := m.nowMS()
	code, err := m.drawCode()
	if err != nil {
		return unavailable()
	}
	poll := make([]byte, pollTokenSize)
	if _, err := io.ReadFull(m.rand, poll); err != nil {
		return unavailable()
	}
	id, err := m.randomID()
	if err != nil {
		return unavailable()
	}
	expiresMS := nowMS + pairingTTL.Milliseconds()
	p := Pairing{
		ID:         id,
		CodeHash:   hashHex([]byte(code)),
		PollHash:   hashHex(poll),
		Name:       name,
		ClientHash: hashHex([]byte(m.clientIP(req.Header))),
		CreatedMS:  nowMS,
		ExpiresMS:  expiresMS,
		ApprovedBy: nil,
		MachineID:  nil,
		Consumed:   false,
	}
	// The reservation is atomic in the store: the expired pairings
	// go away first and do not count as pending, and the row enters
	// only while the pending bound of the hub and the per-client
	// budget both have room, between instances and between concurrent
	// requests. Without a row the answer is 429, and a failure of the
	// mechanism is a 503, without proceeding.
	reserved, err := m.machines.ReservePairing(ctx, p, nowMS)
	if err != nil {
		return unavailable()
	}
	if !reserved {
		return errResponse(429, "rate_limited")
	}
	out, _ := json.Marshal(struct {
		Code      string `json:"code"`
		PollToken string `json:"poll_token"`
		ExpiresMS int64  `json:"expires_ms"`
	}{Code: displayCode(code), PollToken: b64urlNoPad.EncodeToString(poll), ExpiresMS: expiresMS})
	return hub.Response{Status: 201, Body: out}
}

// hasControlRune reports whether s holds a control character.
func hasControlRune(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// clientIP is the client of the creation limit: when the hub is
// configured with the trusted proxy header, the first value of the
// header when it validates as an ip; otherwise the ip of the
// connection, the RemoteAddr without the port, that the hub serve
// adapter sets in the RemoteIPHeader header. An absent or invalid
// value leaves the client unknown (the empty string), which holds its
// own per-client budget.
func (m *Service) clientIP(hdr hub.Header) string {
	if m.clientIPHeader != "" {
		if ip := ipOf(hdr.Get(m.clientIPHeader)); ip != "" {
			return ip
		}
	}
	return ipOf(hdr.Get(RemoteIPHeader))
}

// ipOf validates the header value as an ip and answers its canonical
// form, or the empty string.
func ipOf(v string) string {
	if v == "" {
		return ""
	}
	if ip := net.ParseIP(v); ip != nil {
		return ip.String()
	}
	return ""
}

// confirmPairing is the POST /api/pairings/confirm route (session
// required): the developer confirms the code and links the pairing to
// himself.
func (m *Service) confirmPairing(ctx context.Context, req hub.Request) hub.Response {
	u, ok := m.sessionUser(ctx, req)
	if !ok {
		return errResponse(401, "unauthenticated")
	}
	if !m.checkContentType(req) {
		return errResponse(415, "unsupported_media_type")
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := decodeStrict(req.Body, &body); err != nil {
		return badRequest()
	}
	nowMS := m.nowMS()
	// The attempt is reserved in the database before any check, so
	// the per-user budget holds between instances and between
	// concurrent requests; a refused attempt is refused before the
	// lookup. A wrong confirmation keeps the reservation, and a
	// correct one clears it, after the confirmation.
	reserved, err := m.machines.ReserveConfirmError(ctx, u.ID, nowMS)
	if err != nil {
		return unavailable()
	}
	if !reserved {
		return errResponse(429, "rate_limited")
	}
	code := normalizeCode(body.Code)
	if !validCode(code) {
		return errResponse(400, "invalid_code")
	}
	p, found, err := m.machines.FindPairingByCodeHash(ctx, hashHex([]byte(code)))
	if err != nil {
		return unavailable()
	}
	// The reservation stays as the failure of the attempt.
	invalid := func() hub.Response {
		return errResponse(400, "invalid_code")
	}
	if !found || p.Consumed || p.ExpiresMS <= nowMS || p.ApprovedBy != nil {
		return invalid()
	}
	confirmed, err := m.machines.ConfirmPairing(ctx, p.ID, u.ID)
	if err != nil {
		return unavailable()
	}
	if !confirmed {
		// The pairing was confirmed in the window of the call: the
		// same invalid answer, and the attempt counts.
		return invalid()
	}
	// The correct confirmation clears the quota of the user; a
	// failure of the mechanism is not discarded.
	if err := m.machines.ClearPairingErrors(ctx, u.ID, nowMS); err != nil {
		return unavailable()
	}
	return hub.Response{Status: 204}
}

// pollPairing is the POST /api/pairings/poll route (no session): the
// machine polls its poll token. While the pairing is pending it
// receives 202; after the validity 410; with an unknown token 404;
// after the confirmation, exactly on the first poll, the machine id
// and the key; the second poll is 404.
func (m *Service) pollPairing(ctx context.Context, req hub.Request) hub.Response {
	if !m.checkContentType(req) {
		return errResponse(415, "unsupported_media_type")
	}
	var body struct {
		PollToken string `json:"poll_token"`
	}
	if err := decodeStrict(req.Body, &body); err != nil {
		return badRequest()
	}
	token, err := b64urlNoPad.DecodeString(body.PollToken)
	if err != nil || len(token) != pollTokenSize {
		return errResponse(404, "not_found")
	}
	p, found, err := m.machines.FindPairingByPollHash(ctx, hashHex(token))
	if err != nil {
		return unavailable()
	}
	if !found {
		return errResponse(404, "not_found")
	}
	if p.Consumed {
		// Delivered once: the second and later polls are 404.
		return errResponse(404, "not_found")
	}
	nowMS := m.nowMS()
	if p.ExpiresMS <= nowMS {
		return errResponse(410, "expired")
	}
	if p.ApprovedBy == nil {
		pending, _ := json.Marshal(struct {
			Status string `json:"status"`
		}{Status: "pending"})
		return hub.Response{Status: 202, Body: pending}
	}
	// The pairing was confirmed: deliver the key, exactly once.
	key := make([]byte, machineKeySize)
	if _, err := io.ReadFull(m.rand, key); err != nil {
		return unavailable()
	}
	id, err := m.randomID()
	if err != nil {
		return unavailable()
	}
	mc := Machine{
		ID:          id,
		DeveloperID: *p.ApprovedBy,
		Name:        p.Name,
		KeyHash:     hashHex(key),
		CreatedMS:   nowMS,
		RevokedMS:   nil,
	}
	delivered, err := m.machines.DeliverMachine(ctx, p.ID, mc)
	if err != nil {
		return unavailable()
	}
	if !delivered {
		return errResponse(404, "not_found")
	}
	out, _ := json.Marshal(struct {
		MachineID string `json:"machine_id"`
		Key       string `json:"key"`
	}{MachineID: id, Key: b64urlNoPad.EncodeToString(key)})
	return hub.Response{Status: 200, Body: out}
}
