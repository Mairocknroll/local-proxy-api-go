package lpr

import (
	"testing"
	"time"
)

func TestMemoryStoreRecentEventsKeepsNewestFirst(t *testing.T) {
	store := NewMemoryStore(3)

	for i := 1; i <= 5; i++ {
		store.AddEvent(LprEvent{
			ID:          string(rune('0' + i)),
			EventStatus: StatusReadOK,
			SourceType:  SourceLPRHook,
			CreatedAt:   time.Unix(int64(i), 0),
		})
	}

	got := store.RecentEvents(10)
	if len(got) != 3 {
		t.Fatalf("RecentEvents len = %d, want 3", len(got))
	}
	want := []string{"5", "4", "3"}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("RecentEvents[%d].ID = %q, want %q", i, got[i].ID, want[i])
		}
	}
}

func TestMemoryStoreCameraStatus(t *testing.T) {
	store := NewMemoryStore(10)
	event := LprEvent{
		ID:           "event-1",
		CameraIP:     "192.0.2.10",
		GateNo:       "1",
		Direction:    "ENT",
		PlateText:    "ABC123",
		EventStatus:  StatusReadOK,
		ErrorMessage: "",
		CreatedAt:    time.Unix(100, 0),
	}

	store.UpdateCameraStatus(event)
	statuses := store.CameraStatuses()
	if len(statuses) != 1 {
		t.Fatalf("CameraStatuses len = %d, want 1", len(statuses))
	}
	if statuses[0].CameraID != "192.0.2.10" {
		t.Fatalf("CameraID = %q, want camera IP fallback", statuses[0].CameraID)
	}
	if statuses[0].HealthStatus != "online" {
		t.Fatalf("HealthStatus = %q, want online", statuses[0].HealthStatus)
	}
}

func TestMemoryStoreDedupesPendingByEvent(t *testing.T) {
	store := NewMemoryStore(10)
	review := LprPendingReview{
		EventID:   "event-1",
		CameraID:  "camera-1",
		GateNo:    "1",
		Reason:    string(StatusNoPlate),
		Status:    PendingStatusPending,
		CreatedAt: time.Now(),
	}

	first := store.AddPending(review)
	second := store.AddPending(review)

	if first.ID == "" {
		t.Fatal("first pending ID is empty")
	}
	if second.ID != first.ID {
		t.Fatalf("duplicate pending ID = %q, want %q", second.ID, first.ID)
	}
	if got := store.PendingReviews(10); len(got) != 1 {
		t.Fatalf("pending len = %d, want 1", len(got))
	}
}

func TestMemoryStoreManualConfirmAndCorrection(t *testing.T) {
	store := NewMemoryStore(10)
	pending := store.AddPending(LprPendingReview{
		EventID: "event-1",
		Reason:  string(StatusNoPlate),
		Status:  PendingStatusPending,
	})

	now := time.Now()
	pending.Status = PendingStatusManualConfirmed
	pending.CorrectedPlate = "ABC123"
	pending.CorrectedBy = "operator-1"
	pending.CorrectedAt = &now
	if !store.UpdatePending(pending) {
		t.Fatal("UpdatePending returned false")
	}

	store.AddManualCorrection(LprManualCorrection{
		PendingID:      pending.ID,
		EventID:        pending.EventID,
		CorrectedPlate: pending.CorrectedPlate,
		CorrectedBy:    pending.CorrectedBy,
	})

	updated, ok := store.GetPending(pending.ID)
	if !ok {
		t.Fatal("pending not found")
	}
	if updated.Status != PendingStatusManualConfirmed {
		t.Fatalf("pending status = %q, want %q", updated.Status, PendingStatusManualConfirmed)
	}
	if got := store.ManualCorrections(10); len(got) != 1 {
		t.Fatalf("manual corrections len = %d, want 1", len(got))
	}
}

func TestMemoryStoreRejectPending(t *testing.T) {
	store := NewMemoryStore(10)
	pending := store.AddPending(LprPendingReview{EventID: "event-1", Status: PendingStatusPending})

	if !store.RejectPending(pending.ID, "operator rejected") {
		t.Fatal("RejectPending returned false")
	}

	updated, ok := store.GetPending(pending.ID)
	if !ok {
		t.Fatal("pending not found")
	}
	if updated.Status != PendingStatusRejected {
		t.Fatalf("pending status = %q, want %q", updated.Status, PendingStatusRejected)
	}
}
