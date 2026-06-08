package lpr

import (
	"errors"
	"testing"
	"time"

	"GO_LANG_WORKSPACE/internal/ws"
)

func TestClassifyStatus(t *testing.T) {
	tests := []struct {
		name  string
		input ObserveInput
		want  EventStatus
	}{
		{name: "duplicate", input: ObserveInput{PlateText: "ABC123", Duplicate: true}, want: StatusDuplicate},
		{name: "multipart error", input: ObserveInput{MultipartError: errors.New("bad multipart")}, want: StatusMultipartError},
		{name: "parse error", input: ObserveInput{ParseError: errors.New("bad xml")}, want: StatusParseError},
		{name: "upstream error", input: ObserveInput{PlateText: "ABC123", UpstreamError: errors.New("cloud down")}, want: StatusUpstreamError},
		{name: "no plate", input: ObserveInput{}, want: StatusNoPlate},
		{name: "unknown plate", input: ObserveInput{PlateText: "unknown"}, want: StatusUnknownPlate},
		{name: "read ok", input: ObserveInput{PlateText: "ABC123"}, want: StatusReadOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyStatus(tt.input); got != tt.want {
				t.Fatalf("ClassifyStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestObserverCreatesPendingForProblematicStatuses(t *testing.T) {
	tests := []struct {
		name  string
		input ObserveInput
		want  EventStatus
	}{
		{name: "no plate", input: ObserveInput{Endpoint: "test", GateNo: "1"}, want: StatusNoPlate},
		{name: "unknown", input: ObserveInput{Endpoint: "test", GateNo: "1", PlateText: "unknown"}, want: StatusUnknownPlate},
		{name: "parse error", input: ObserveInput{Endpoint: "test", GateNo: "1", ParseError: errors.New("bad xml")}, want: StatusParseError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewMemoryStore(10)
			observer := NewObserver(store, nil, true)
			observer.process(tt.input)

			pending := store.PendingReviews(10)
			if len(pending) != 1 {
				t.Fatalf("pending len = %d, want 1", len(pending))
			}
			if pending[0].Reason != string(tt.want) {
				t.Fatalf("pending reason = %q, want %q", pending[0].Reason, tt.want)
			}
		})
	}
}

func TestObserverDoesNotCreatePendingForReadOK(t *testing.T) {
	store := NewMemoryStore(10)
	observer := NewObserver(store, nil, true)

	observer.process(ObserveInput{Endpoint: "test", GateNo: "1", PlateText: "ABC123"})

	if got := store.PendingReviews(10); len(got) != 0 {
		t.Fatalf("pending len = %d, want 0", len(got))
	}
}

func TestObserverWorkerRecoversFromPanic(t *testing.T) {
	observer := &Observer{
		enabled:   true,
		hub:       &ws.Hub{},
		events:    make(chan ObserveInput, 2),
		broadcast: make(chan any),
		seen:      make(map[string]time.Time),
		seenTTL:   defaultDuplicateTTL,
	}
	close(observer.broadcast)

	observer.events <- ObserveInput{Endpoint: "test", GateNo: "1", PlateText: "ABC123"}
	close(observer.events)

	observer.runObserve()
}
