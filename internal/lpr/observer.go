package lpr

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"GO_LANG_WORKSPACE/internal/ws"

	"github.com/google/uuid"
)

const (
	defaultObserveBuffer   = 512
	defaultBroadcastBuffer = 256
	defaultDuplicateTTL    = 30 * time.Second
)

type ObserveInput struct {
	Endpoint     string
	RequestID    string
	CameraID     string
	CameraIP     string
	GateNo       string
	Direction    string
	UUID         string
	PlateText    string
	RawPlateText string
	Confidence   *float64
	VehicleType  string
	SourceType   SourceType

	MultipartError error
	ParseError     error
	UpstreamError  error
	Error          error
	Duplicate      bool
	CreatedAt      time.Time
}

type Observer struct {
	enabled bool
	store   *MemoryStore
	hub     *ws.Hub

	events    chan ObserveInput
	broadcast chan any

	mu       sync.Mutex
	seen     map[string]time.Time
	seenTTL  time.Duration
	stopOnce sync.Once
}

func NewObserver(store *MemoryStore, hub *ws.Hub, enabled bool) *Observer {
	o := &Observer{
		enabled:   enabled,
		store:     store,
		hub:       hub,
		events:    make(chan ObserveInput, defaultObserveBuffer),
		broadcast: make(chan any, defaultBroadcastBuffer),
		seen:      make(map[string]time.Time),
		seenTTL:   defaultDuplicateTTL,
	}
	if enabled {
		go o.runObserve()
		if hub != nil {
			go o.runBroadcast()
		}
	}
	return o
}

func (o *Observer) ObserveHook(ctx context.Context, input ObserveInput) {
	if o == nil || !o.enabled {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[LPR][observer] recovered: %v", r)
		}
	}()

	if ctx != nil {
		select {
		case <-ctx.Done():
			return
		default:
		}
	}

	select {
	case o.events <- input:
	default:
		log.Printf("[LPR][observer] observe queue full; dropping hook endpoint=%s gate=%s", input.Endpoint, input.GateNo)
	}
}

func (o *Observer) runObserve() {
	for input := range o.events {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[LPR][observer] worker recovered: %v", r)
				}
			}()
			o.process(input)
		}()
	}
}

func (o *Observer) process(input ObserveInput) {
	if input.SourceType == "" {
		input.SourceType = SourceLPRHook
	}
	if input.CreatedAt.IsZero() {
		input.CreatedAt = time.Now()
	}
	if input.RawPlateText == "" {
		input.RawPlateText = input.PlateText
	}

	input.Duplicate = input.Duplicate || o.isDuplicate(input)
	status := ClassifyStatus(input)
	errMsg := errorMessage(input)

	event := LprEvent{
		ID:           uuid.NewString(),
		UUID:         input.UUID,
		RequestID:    input.RequestID,
		Endpoint:     input.Endpoint,
		CameraID:     cameraIDFromInput(input),
		CameraIP:     input.CameraIP,
		GateNo:       input.GateNo,
		Direction:    input.Direction,
		PlateText:    strings.TrimSpace(input.PlateText),
		RawPlateText: input.RawPlateText,
		Confidence:   input.Confidence,
		EventStatus:  status,
		VehicleType:  input.VehicleType,
		SourceType:   input.SourceType,
		ErrorMessage: errMsg,
		CreatedAt:    input.CreatedAt,
	}

	if o.store != nil {
		o.store.AddEvent(event)
		o.store.UpdateCameraStatus(event)
	}

	var pending *LprPendingReview
	if o.store != nil && IsPendingReviewStatus(status) {
		review := o.store.AddPending(pendingFromEvent(event))
		pending = &review
	}

	if o.hub != nil {
		o.enqueueMonitor(event)
		if pending != nil {
			o.enqueueMonitor(MonitorUpdate{
				Type:      MonitorUpdatePendingCreated,
				Data:      *pending,
				CreatedAt: time.Now(),
			})
		}
	}
}

func ClassifyStatus(input ObserveInput) EventStatus {
	switch {
	case input.Duplicate:
		return StatusDuplicate
	case input.MultipartError != nil:
		return StatusMultipartError
	case input.ParseError != nil:
		return StatusParseError
	case input.UpstreamError != nil:
		return StatusUpstreamError
	}

	plate := strings.TrimSpace(input.PlateText)
	if plate == "" {
		return StatusNoPlate
	}
	switch strings.ToLower(plate) {
	case "unknown", "no plate", "noplate", "no_plate", "unreadable":
		return StatusUnknownPlate
	default:
		return StatusReadOK
	}
}

func (o *Observer) runBroadcast() {
	for msg := range o.broadcast {
		if o.hub == nil {
			continue
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[LPR][observer] broadcast recovered: %v", r)
				}
			}()
			b, err := json.Marshal(msg)
			if err != nil {
				log.Printf("[LPR][observer] marshal monitor event: %v", err)
				return
			}
			o.hub.Broadcast(MonitorGroup, b)
		}()
	}
}

func (o *Observer) BroadcastMonitorUpdate(updateType string, data any) {
	if o == nil || !o.enabled || o.hub == nil {
		return
	}
	o.enqueueMonitor(MonitorUpdate{
		Type:      updateType,
		Data:      data,
		CreatedAt: time.Now(),
	})
}

func (o *Observer) Enabled() bool {
	return o != nil && o.enabled
}

func (o *Observer) enqueueMonitor(msg any) {
	select {
	case o.broadcast <- msg:
	default:
		log.Printf("[LPR][observer] monitor broadcast queue full; dropping update")
	}
}

func (o *Observer) isDuplicate(input ObserveInput) bool {
	plate := strings.TrimSpace(strings.ToLower(input.PlateText))
	if plate == "" {
		return false
	}
	key := input.Endpoint + "|" + input.CameraIP + "|" + input.GateNo + "|" + input.Direction + "|" + plate

	now := time.Now()
	o.mu.Lock()
	defer o.mu.Unlock()

	if exp, ok := o.seen[key]; ok && exp.After(now) {
		return true
	}
	o.seen[key] = now.Add(o.seenTTL)

	for k, exp := range o.seen {
		if exp.Before(now) {
			delete(o.seen, k)
		}
	}
	return false
}

func cameraIDFromInput(input ObserveInput) string {
	if input.CameraID != "" {
		return input.CameraID
	}
	if input.CameraIP != "" {
		return input.CameraIP
	}
	if input.GateNo != "" || input.Direction != "" || input.Endpoint != "" {
		return input.Endpoint + ":" + input.Direction + ":" + input.GateNo
	}
	return ""
}

func errorMessage(input ObserveInput) string {
	switch {
	case input.MultipartError != nil:
		return input.MultipartError.Error()
	case input.ParseError != nil:
		return input.ParseError.Error()
	case input.UpstreamError != nil:
		return input.UpstreamError.Error()
	case input.Error != nil:
		return input.Error.Error()
	default:
		return ""
	}
}

func IsPendingReviewStatus(status EventStatus) bool {
	switch status {
	case StatusNoPlate, StatusUnknownPlate, StatusParseError, StatusMultipartError, StatusAmbiguous, StatusUpstreamError:
		return true
	default:
		return false
	}
}

func pendingFromEvent(event LprEvent) LprPendingReview {
	now := time.Now()
	return LprPendingReview{
		EventID:     event.ID,
		UUID:        event.UUID,
		CameraID:    event.CameraID,
		CameraIP:    event.CameraIP,
		GateNo:      event.GateNo,
		Direction:   event.Direction,
		PlateText:   event.PlateText,
		VehicleType: event.VehicleType,
		Reason:      string(event.EventStatus),
		Status:      PendingStatusPending,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}
