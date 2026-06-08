package lpr

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type fakeCustomerLookup struct {
	res map[string]any
	err error
}

func (f fakeCustomerLookup) GetCustomerID(_ context.Context, _, _ string) (map[string]any, error) {
	return f.res, f.err
}

type broadcastCall struct {
	group string
	data  []byte
}

type fakeBroadcaster struct {
	calls []broadcastCall
}

func (f *fakeBroadcaster) Broadcast(group string, data []byte) {
	f.calls = append(f.calls, broadcastCall{group: group, data: data})
}

func TestRescueToKioskSucceedsWithCustID(t *testing.T) {
	store := NewMemoryStore(10)
	pending := store.AddPending(freshPending())
	broadcaster := &fakeBroadcaster{}
	handler := NewHandler(store, nil, nil, broadcaster, fakeCustomerLookup{res: map[string]any{"cust_id": "cust-1"}}, "park-1", true, 30*time.Second)

	rec := performRescue(handler, pending.ID, `{"corrected_plate":"ABC123","operator":"operator-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if len(broadcaster.calls) != 1 {
		t.Fatalf("broadcast calls = %d, want 1", len(broadcaster.calls))
	}
	if broadcaster.calls[0].group != "gate_in_1" {
		t.Fatalf("broadcast group = %q, want gate_in_1", broadcaster.calls[0].group)
	}

	payload := map[string]any{}
	if err := json.Unmarshal(broadcaster.calls[0].data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["license_plate"] != "ABC123" {
		t.Fatalf("license_plate = %v, want ABC123", payload["license_plate"])
	}
	wantKeys := []string{"license_plate", "uuid", "time_in", "cust_id", "ef_id", "vehicle_type", "license_plate_img_base64"}
	if len(payload) != len(wantKeys) {
		t.Fatalf("payload keys = %v, want only %v", payload, wantKeys)
	}
	for _, key := range wantKeys {
		if _, ok := payload[key]; !ok {
			t.Fatalf("payload missing key %q: %v", key, payload)
		}
	}

	updated, _ := store.GetPending(pending.ID)
	if updated.Status != PendingStatusRescuedToKiosk {
		t.Fatalf("pending status = %q, want %q", updated.Status, PendingStatusRescuedToKiosk)
	}
}

func TestRescueToKioskSucceedsWithEFID(t *testing.T) {
	store := NewMemoryStore(10)
	pending := store.AddPending(freshPending())
	broadcaster := &fakeBroadcaster{}
	handler := NewHandler(store, nil, nil, broadcaster, fakeCustomerLookup{res: map[string]any{"ef_id": "ef-1"}}, "park-1", true, 30*time.Second)

	rec := performRescue(handler, pending.ID, `{"corrected_plate":"ABC123","operator":"operator-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if len(broadcaster.calls) != 1 {
		t.Fatalf("broadcast calls = %d, want 1", len(broadcaster.calls))
	}
}

func TestRescueToKioskParsesEntitlementUnderData(t *testing.T) {
	store := NewMemoryStore(10)
	pending := store.AddPending(freshPending())
	broadcaster := &fakeBroadcaster{}
	handler := NewHandler(store, nil, nil, broadcaster, fakeCustomerLookup{res: map[string]any{"data": map[string]any{"cust_id": "cust-1", "ef_id": "ef-1"}}}, "park-1", true, 30*time.Second)

	rec := performRescue(handler, pending.ID, `{"corrected_plate":"ABC123","operator":"operator-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if len(broadcaster.calls) != 1 {
		t.Fatalf("broadcast calls = %d, want 1", len(broadcaster.calls))
	}
}

func TestRescueToKioskDoesNotBroadcastWhenNotMember(t *testing.T) {
	store := NewMemoryStore(10)
	pending := store.AddPending(freshPending())
	broadcaster := &fakeBroadcaster{}
	handler := NewHandler(store, nil, nil, broadcaster, fakeCustomerLookup{res: map[string]any{"status": false}}, "park-1", true, 30*time.Second)

	rec := performRescue(handler, pending.ID, `{"corrected_plate":"ABC123","operator":"operator-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if len(broadcaster.calls) != 0 {
		t.Fatalf("broadcast calls = %d, want 0", len(broadcaster.calls))
	}
	updated, _ := store.GetPending(pending.ID)
	if updated.Status != PendingStatusValidatedNotMember {
		t.Fatalf("pending status = %q, want %q", updated.Status, PendingStatusValidatedNotMember)
	}
}

func TestRescueToKioskRejectsExpiredPending(t *testing.T) {
	store := NewMemoryStore(10)
	p := freshPending()
	p.CreatedAt = time.Now().Add(-2 * time.Minute)
	pending := store.AddPending(p)
	broadcaster := &fakeBroadcaster{}
	handler := NewHandler(store, nil, nil, broadcaster, fakeCustomerLookup{res: map[string]any{"cust_id": "cust-1"}}, "park-1", true, 30*time.Second)

	rec := performRescue(handler, pending.ID, `{"corrected_plate":"ABC123","operator":"operator-1"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if len(broadcaster.calls) != 0 {
		t.Fatalf("broadcast calls = %d, want 0", len(broadcaster.calls))
	}
	updated, _ := store.GetPending(pending.ID)
	if updated.Status != PendingStatusRescueExpired {
		t.Fatalf("pending status = %q, want %q", updated.Status, PendingStatusRescueExpired)
	}
}

func TestRescueToKioskRejectsEmptyPlate(t *testing.T) {
	store := NewMemoryStore(10)
	pending := store.AddPending(freshPending())
	handler := NewHandler(store, nil, nil, &fakeBroadcaster{}, fakeCustomerLookup{res: map[string]any{"cust_id": "cust-1"}}, "park-1", true, 30*time.Second)

	rec := performRescue(handler, pending.ID, `{"corrected_plate":" ","operator":"operator-1"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestRescueToKioskRejectsTerminalPending(t *testing.T) {
	terminalStatuses := []string{PendingStatusRejected, PendingStatusRescuedToKiosk}
	for _, status := range terminalStatuses {
		t.Run(status, func(t *testing.T) {
			store := NewMemoryStore(10)
			p := freshPending()
			p.Status = status
			pending := store.AddPending(p)
			broadcaster := &fakeBroadcaster{}
			handler := NewHandler(store, nil, nil, broadcaster, fakeCustomerLookup{res: map[string]any{"cust_id": "cust-1"}}, "park-1", true, 30*time.Second)

			rec := performRescue(handler, pending.ID, `{"corrected_plate":"ABC123","operator":"operator-1"}`)
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409", rec.Code)
			}
			if len(broadcaster.calls) != 0 {
				t.Fatalf("broadcast calls = %d, want 0", len(broadcaster.calls))
			}
		})
	}
}

func TestClaimUnlockedPendingSucceeds(t *testing.T) {
	store := NewMemoryStore(10)
	pending := store.AddPending(freshPending())
	handler := NewHandler(store, nil, nil, nil, nil, "park-1", true, 30*time.Second)

	rec := performClaim(handler, pending.ID, `{"operator":"operator-1","ttl_seconds":30}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	updated, _ := store.GetPending(pending.ID)
	if updated.LockedBy != "operator-1" {
		t.Fatalf("locked_by = %q, want operator-1", updated.LockedBy)
	}
	if updated.LockedUntil == nil || !updated.LockedUntil.After(time.Now()) {
		t.Fatalf("locked_until = %v, want future time", updated.LockedUntil)
	}
	if updated.Version != 1 {
		t.Fatalf("version = %d, want 1", updated.Version)
	}
}

func TestClaimLockedPendingByAnotherOperatorReturnsConflict(t *testing.T) {
	store := NewMemoryStore(10)
	lockedUntil := time.Now().Add(time.Minute)
	pending := freshPending()
	pending.LockedBy = "operator-1"
	pending.LockedUntil = &lockedUntil
	pending = store.AddPending(pending)
	handler := NewHandler(store, nil, nil, nil, nil, "park-1", true, 30*time.Second)

	rec := performClaim(handler, pending.ID, `{"operator":"operator-2","ttl_seconds":30}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestForceTakeoverClaimSucceeds(t *testing.T) {
	store := NewMemoryStore(10)
	lockedUntil := time.Now().Add(time.Minute)
	pending := freshPending()
	pending.LockedBy = "operator-1"
	pending.LockedUntil = &lockedUntil
	pending = store.AddPending(pending)
	handler := NewHandler(store, nil, nil, nil, nil, "park-1", true, 30*time.Second)

	rec := performClaim(handler, pending.ID, `{"operator":"operator-2","ttl_seconds":30,"force":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	updated, _ := store.GetPending(pending.ID)
	if updated.LockedBy != "operator-2" {
		t.Fatalf("locked_by = %q, want operator-2", updated.LockedBy)
	}
}

func TestReleaseByNonOwnerReturnsConflict(t *testing.T) {
	store := NewMemoryStore(10)
	lockedUntil := time.Now().Add(time.Minute)
	pending := freshPending()
	pending.LockedBy = "operator-1"
	pending.LockedUntil = &lockedUntil
	pending = store.AddPending(pending)
	handler := NewHandler(store, nil, nil, nil, nil, "park-1", true, 30*time.Second)

	rec := performRelease(handler, pending.ID, `{"operator":"operator-2"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestReleaseExpiredLockByNonOwnerReturnsConflict(t *testing.T) {
	store := NewMemoryStore(10)
	lockedUntil := time.Now().Add(-time.Minute)
	pending := freshPending()
	pending.LockedBy = "operator-1"
	pending.LockedUntil = &lockedUntil
	pending = store.AddPending(pending)
	handler := NewHandler(store, nil, nil, nil, nil, "park-1", true, 30*time.Second)

	rec := performRelease(handler, pending.ID, `{"operator":"operator-2"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestExpiredLockAllowsNewClaim(t *testing.T) {
	store := NewMemoryStore(10)
	lockedUntil := time.Now().Add(-time.Minute)
	pending := freshPending()
	pending.LockedBy = "operator-1"
	pending.LockedUntil = &lockedUntil
	pending = store.AddPending(pending)
	handler := NewHandler(store, nil, nil, nil, nil, "park-1", true, 30*time.Second)

	rec := performClaim(handler, pending.ID, `{"operator":"operator-2","ttl_seconds":30}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	updated, _ := store.GetPending(pending.ID)
	if updated.LockedBy != "operator-2" {
		t.Fatalf("locked_by = %q, want operator-2", updated.LockedBy)
	}
}

func TestStaleVersionMutationReturnsConflict(t *testing.T) {
	store := NewMemoryStore(10)
	pending := freshPending()
	pending.Version = 1
	pending = store.AddPending(pending)
	broadcaster := &fakeBroadcaster{}
	handler := NewHandler(store, nil, nil, broadcaster, fakeCustomerLookup{res: map[string]any{"cust_id": "cust-1"}}, "park-1", true, 30*time.Second)

	rec := performRescue(handler, pending.ID, `{"corrected_plate":"ABC123","operator":"operator-1","version":0}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if len(broadcaster.calls) != 0 {
		t.Fatalf("broadcast calls = %d, want 0", len(broadcaster.calls))
	}
}

func TestRescueToKioskRetryDoesNotBroadcastAgain(t *testing.T) {
	store := NewMemoryStore(10)
	pending := store.AddPending(freshPending())
	broadcaster := &fakeBroadcaster{}
	handler := NewHandler(store, nil, nil, broadcaster, fakeCustomerLookup{res: map[string]any{"cust_id": "cust-1"}}, "park-1", true, 30*time.Second)

	first := performRescue(handler, pending.ID, `{"corrected_plate":"ABC123","operator":"operator-1","version":0}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, body=%s", first.Code, first.Body.String())
	}
	second := performRescue(handler, pending.ID, `{"corrected_plate":"ABC123","operator":"operator-1","version":0}`)
	if second.Code != http.StatusConflict {
		t.Fatalf("second status = %d, want 409", second.Code)
	}
	if len(broadcaster.calls) != 1 {
		t.Fatalf("broadcast calls = %d, want 1", len(broadcaster.calls))
	}
}

func TestRejectFinalStateReturnsConflict(t *testing.T) {
	store := NewMemoryStore(10)
	pending := freshPending()
	pending.Status = PendingStatusRescuedToKiosk
	pending = store.AddPending(pending)
	handler := NewHandler(store, nil, nil, nil, nil, "park-1", true, 30*time.Second)

	rec := performReject(handler, pending.ID, `{"operator":"operator-1","version":0}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestRescueToKioskDoesNotReferenceDirectBarrierOrOrderSession(t *testing.T) {
	files := []string{"handler.go", "model.go", "store_memory.go", "cloud_client.go"}
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, forbidden := range []string{"barrier_v2", "OpenBarrier", "CreateOrder", "CreateSession", "NewSession", "AddSession"} {
			if strings.Contains(src, forbidden) {
				t.Fatalf("%s contains forbidden direct action reference %q", file, forbidden)
			}
		}
	}
}

func performRescue(handler *Handler, pendingID, body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v2-202402/lpr/pending/:pendingId/rescue-to-kiosk", handler.RescueToKiosk)

	req := httptest.NewRequest(http.MethodPost, "/api/v2-202402/lpr/pending/"+pendingID+"/rescue-to-kiosk", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func performClaim(handler *Handler, pendingID, body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v2-202402/lpr/pending/:pendingId/claim", handler.ClaimPending)

	req := httptest.NewRequest(http.MethodPost, "/api/v2-202402/lpr/pending/"+pendingID+"/claim", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func performRelease(handler *Handler, pendingID, body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v2-202402/lpr/pending/:pendingId/release", handler.ReleasePending)

	req := httptest.NewRequest(http.MethodPost, "/api/v2-202402/lpr/pending/"+pendingID+"/release", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func performReject(handler *Handler, pendingID, body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v2-202402/lpr/pending/:pendingId/reject", handler.RejectPending)

	req := httptest.NewRequest(http.MethodPost, "/api/v2-202402/lpr/pending/"+pendingID+"/reject", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func freshPending() LprPendingReview {
	return LprPendingReview{
		EventID:     "event-1",
		UUID:        "uuid-1",
		GateNo:      "1",
		Direction:   "ENT",
		Reason:      string(StatusNoPlate),
		Status:      PendingStatusPending,
		VehicleType: "car",
		CreatedAt:   time.Now(),
	}
}
