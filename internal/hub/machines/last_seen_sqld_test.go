//go:build sqld

package machines

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/hub/httpserve"
	"github.com/djalmajr/nau/internal/hub/hubtest"
	"github.com/djalmajr/nau/internal/hub/store"
)

type machineSummary struct {
	ID         string
	Name       string
	Revoked    bool
	LastSeenMS *int64
	Online     bool
}

func mustFindMachine(t *testing.T, list machineListBody, id string) machineSummary {
	t.Helper()
	for _, m := range list.Machines {
		if m.ID == id {
			return machineSummary{
				ID:         m.ID,
				Name:       m.Name,
				Revoked:    m.Revoked,
				LastSeenMS: m.LastSeenMS,
				Online:     m.Online,
			}
		}
	}
	t.Fatalf("machine %s not found in list (%d machines)", id, len(list.Machines))
	return machineSummary{}
}

func formatMS(v *int64) string {
	if v == nil {
		return "nil"
	}
	return strconv.FormatInt(*v, 10)
}

func TestSQLMachineLastSeenPerMachine(t *testing.T) {
	st := hubtest.SQLD(t)
	t.Cleanup(func() {
		stmts := make([]store.Stmt, 0, len(sqldMachineTables))
		for _, table := range sqldMachineTables {
			stmts = append(stmts, store.Stmt{SQL: "DELETE FROM " + table})
		}
		if _, err := st.Batch(context.Background(), stmts); err != nil {
			t.Errorf("cleanup of tables: %v", err)
		}
	})

	t.Run("Visible", func(t *testing.T) {
		s := freshSQLScenario(t, st)
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()
		s.ctx = ctx
		h := httpserve.New(s.srv)

		dA, _ := s.deliver(t, "alice", "laptop-a")
		dB, _ := s.deliver(t, "alice", "laptop-b")
		dC, _ := s.deliver(t, "alice", "laptop-c")

		getMachines := func(user string) machineListBody {
			req := httptest.NewRequest(http.MethodGet, "/api/machines", nil).WithContext(ctx)
			req.AddCookie(&http.Cookie{Name: "nau_session", Value: s.cookies[user]})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /api/machines for %s: status %d, body %s", user, rec.Code, rec.Body.String())
			}
			return parseMachineList(t, rec.Body.Bytes())
		}

		initial := getMachines("alice")
		mA := mustFindMachine(t, initial, dA.MachineID)
		mB := mustFindMachine(t, initial, dB.MachineID)
		mC := mustFindMachine(t, initial, dC.MachineID)
		if mA.LastSeenMS != nil || mA.Online {
			t.Fatalf("machine A before ingest: last_seen_ms=%v online=%v, want nil/false", formatMS(mA.LastSeenMS), mA.Online)
		}
		if mB.LastSeenMS != nil || mB.Online {
			t.Fatalf("machine B before ingest: last_seen_ms=%v online=%v, want nil/false", formatMS(mB.LastSeenMS), mB.Online)
		}
		if mC.LastSeenMS != nil || mC.Online {
			t.Fatalf("control machine C before ingest: last_seen_ms=%v online=%v, want nil/false", formatMS(mC.LastSeenMS), mC.Online)
		}

		t1 := s.nowMS()
		if r := s.ingestWith(dA.Key, sharingInventoryBody(t1)); r.Status != http.StatusNoContent {
			t.Fatalf("ingest machine A: status %d, want 204", r.Status)
		}
		s.advance(10 * time.Second)
		t2 := s.nowMS()
		if r := s.ingestWith(dB.Key, sharingInventoryBody(t2)); r.Status != http.StatusNoContent {
			t.Fatalf("ingest machine B: status %d, want 204", r.Status)
		}

		list1 := getMachines("alice")
		mA = mustFindMachine(t, list1, dA.MachineID)
		mB = mustFindMachine(t, list1, dB.MachineID)
		mC = mustFindMachine(t, list1, dC.MachineID)
		if mA.LastSeenMS == nil || *mA.LastSeenMS != t1 || !mA.Online {
			t.Fatalf("stage 1 machine A: last_seen_ms=%v online=%v, want %d/true", formatMS(mA.LastSeenMS), mA.Online, t1)
		}
		if mB.LastSeenMS == nil || *mB.LastSeenMS != t2 || !mB.Online {
			t.Fatalf("stage 1 machine B: last_seen_ms=%v online=%v, want %d/true", formatMS(mB.LastSeenMS), mB.Online, t2)
		}
		if mC.LastSeenMS != nil || mC.Online {
			t.Fatalf("stage 1 control machine C: last_seen_ms=%v online=%v, want nil/false", formatMS(mC.LastSeenMS), mC.Online)
		}

		s.advance(10 * time.Second)
		t3 := s.nowMS()
		if r := s.ingestWith(dA.Key, sharingInventoryBody(t3)); r.Status != http.StatusNoContent {
			t.Fatalf("ingest machine A second time: status %d, want 204", r.Status)
		}

		list2 := getMachines("alice")
		mA = mustFindMachine(t, list2, dA.MachineID)
		mB = mustFindMachine(t, list2, dB.MachineID)
		mC = mustFindMachine(t, list2, dC.MachineID)
		if mA.LastSeenMS == nil || *mA.LastSeenMS != t3 || !mA.Online {
			t.Fatalf("stage 2 machine A: last_seen_ms=%v online=%v, want %d/true", formatMS(mA.LastSeenMS), mA.Online, t3)
		}
		if mB.LastSeenMS == nil || *mB.LastSeenMS != t2 || !mB.Online {
			t.Fatalf("stage 2 machine B: last_seen_ms=%v online=%v, want %d/true", formatMS(mB.LastSeenMS), mB.Online, t2)
		}
		if mC.LastSeenMS != nil || mC.Online {
			t.Fatalf("stage 2 control machine C: last_seen_ms=%v online=%v, want nil/false", formatMS(mC.LastSeenMS), mC.Online)
		}

		s.advance(70 * time.Second)
		list3 := getMachines("alice")
		mA = mustFindMachine(t, list3, dA.MachineID)
		mB = mustFindMachine(t, list3, dB.MachineID)
		mC = mustFindMachine(t, list3, dC.MachineID)
		if mA.LastSeenMS == nil || *mA.LastSeenMS != t3 || mA.Online {
			t.Fatalf("stale machine A: last_seen_ms=%v online=%v, want %d/false", formatMS(mA.LastSeenMS), mA.Online, t3)
		}
		if mB.LastSeenMS == nil || *mB.LastSeenMS != t2 || mB.Online {
			t.Fatalf("stale machine B: last_seen_ms=%v online=%v, want %d/false", formatMS(mB.LastSeenMS), mB.Online, t2)
		}
		if mC.LastSeenMS != nil || mC.Online {
			t.Fatalf("stale control machine C: last_seen_ms=%v online=%v, want nil/false", formatMS(mC.LastSeenMS), mC.Online)
		}

		t4 := s.nowMS()
		if r := s.ingestWith(dB.Key, sharingInventoryBody(t4)); r.Status != http.StatusNoContent {
			t.Fatalf("refresh machine B: status %d, want 204", r.Status)
		}

		list4 := getMachines("alice")
		mA = mustFindMachine(t, list4, dA.MachineID)
		mB = mustFindMachine(t, list4, dB.MachineID)
		mC = mustFindMachine(t, list4, dC.MachineID)
		if mA.LastSeenMS == nil || *mA.LastSeenMS != t3 || mA.Online {
			t.Fatalf("independent stale machine A: last_seen_ms=%v online=%v, want %d/false", formatMS(mA.LastSeenMS), mA.Online, t3)
		}
		if mB.LastSeenMS == nil || *mB.LastSeenMS != t4 || !mB.Online {
			t.Fatalf("refreshed machine B: last_seen_ms=%v online=%v, want %d/true", formatMS(mB.LastSeenMS), mB.Online, t4)
		}
		if mC.LastSeenMS != nil || mC.Online {
			t.Fatalf("control machine C: last_seen_ms=%v online=%v, want nil/false", formatMS(mC.LastSeenMS), mC.Online)
		}
	})

	t.Run("OwnRevoked", func(t *testing.T) {
		s := freshSQLScenario(t, st)
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()
		s.ctx = ctx
		h := httpserve.New(s.srv)

		getMachines := func(user string) machineListBody {
			req := httptest.NewRequest(http.MethodGet, "/api/machines", nil).WithContext(ctx)
			req.AddCookie(&http.Cookie{Name: "nau_session", Value: s.cookies[user]})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /api/machines for %s: status %d, body %s", user, rec.Code, rec.Body.String())
			}
			return parseMachineList(t, rec.Body.Bytes())
		}

		dA, _ := s.deliver(t, "alice", "laptop-revoked-a")
		dB, _ := s.deliver(t, "alice", "laptop-revoked-b")
		dC, _ := s.deliver(t, "alice", "laptop-revoked-c")

		t1 := s.nowMS()
		if r := s.ingestWith(dA.Key, sharingInventoryBody(t1)); r.Status != http.StatusNoContent {
			t.Fatalf("ingest machine A: status %d, want 204", r.Status)
		}
		s.advance(10 * time.Second)
		t2 := s.nowMS()
		if r := s.ingestWith(dB.Key, sharingInventoryBody(t2)); r.Status != http.StatusNoContent {
			t.Fatalf("ingest machine B: status %d, want 204", r.Status)
		}

		for _, id := range []string{dA.MachineID, dB.MachineID, dC.MachineID} {
			r := s.do("DELETE", "/api/machines/"+id, nil, s.asUser("alice"), true)
			if r.Status != http.StatusNoContent {
				t.Fatalf("revoke machine %s: status %d, want 204", id, r.Status)
			}
		}

		list := getMachines("alice")
		mA := mustFindMachine(t, list, dA.MachineID)
		mB := mustFindMachine(t, list, dB.MachineID)
		mC := mustFindMachine(t, list, dC.MachineID)

		if !mA.Revoked || !mB.Revoked || !mC.Revoked {
			t.Fatalf("revoked flags = [%v, %v, %v], want all true", mA.Revoked, mB.Revoked, mC.Revoked)
		}
		if mA.LastSeenMS == nil || *mA.LastSeenMS != t1 || !mA.Online {
			t.Fatalf("revoked machine A: last_seen_ms=%v online=%v, want %d/true", formatMS(mA.LastSeenMS), mA.Online, t1)
		}
		if mB.LastSeenMS == nil || *mB.LastSeenMS != t2 || !mB.Online {
			t.Fatalf("revoked machine B: last_seen_ms=%v online=%v, want %d/true", formatMS(mB.LastSeenMS), mB.Online, t2)
		}
		if mC.LastSeenMS != nil || mC.Online {
			t.Fatalf("revoked control machine C: last_seen_ms=%v online=%v, want nil/false", formatMS(mC.LastSeenMS), mC.Online)
		}

		bobList := getMachines("bob")
		for _, m := range bobList.Machines {
			if m.ID == dA.MachineID || m.ID == dB.MachineID || m.ID == dC.MachineID {
				t.Fatalf("bob must not see revoked machine %s of alice", m.ID)
			}
		}
	})
}
