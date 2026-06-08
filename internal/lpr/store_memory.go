package lpr

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

const defaultRecentCapacity = 500
const pendingDedupeWindow = 10 * time.Second

var errPendingNotFound = errors.New("pending review not found")

type MemoryStore struct {
	mu                sync.RWMutex
	events            []LprEvent
	next              int
	count             int
	cameras           map[string]CameraStatus
	pending           map[string]LprPendingReview
	pendingOrder      []string
	manualCorrections []LprManualCorrection
	capacity          int
}

func NewMemoryStore(capacity int) *MemoryStore {
	if capacity <= 0 {
		capacity = defaultRecentCapacity
	}
	return &MemoryStore{
		events:       make([]LprEvent, capacity),
		cameras:      make(map[string]CameraStatus),
		pending:      make(map[string]LprPendingReview),
		pendingOrder: make([]string, 0, capacity),
		capacity:     capacity,
	}
}

func (s *MemoryStore) AddEvent(event LprEvent) {
	if s == nil {
		return
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.events[s.next] = event
	s.next = (s.next + 1) % s.capacity
	if s.count < s.capacity {
		s.count++
	}
}

func (s *MemoryStore) RecentEvents(limit int) []LprEvent {
	if s == nil {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 || limit > s.count {
		limit = s.count
	}
	out := make([]LprEvent, 0, limit)
	for i := 0; i < limit; i++ {
		idx := (s.next - 1 - i + s.capacity) % s.capacity
		out = append(out, s.events[idx])
	}
	return out
}

func (s *MemoryStore) GetEvent(id string) (LprEvent, bool) {
	if s == nil || id == "" {
		return LprEvent{}, false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	for i := 0; i < s.count; i++ {
		idx := (s.next - 1 - i + s.capacity) % s.capacity
		if s.events[idx].ID == id {
			return s.events[idx], true
		}
	}
	return LprEvent{}, false
}

func (s *MemoryStore) UpdateCameraStatus(event LprEvent) {
	if s == nil {
		return
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now()
	}

	cameraID := cameraKey(event)
	if cameraID == "" {
		return
	}

	health := "online"
	if event.EventStatus == StatusMultipartError || event.EventStatus == StatusParseError || event.EventStatus == StatusUpstreamError {
		health = "warning"
	}

	status := CameraStatus{
		CameraID:         cameraID,
		CameraIP:         event.CameraIP,
		GateNo:           event.GateNo,
		Direction:        event.Direction,
		HealthStatus:     health,
		LastEventID:      event.ID,
		LastEventStatus:  event.EventStatus,
		LastPlateText:    event.PlateText,
		LastErrorMessage: event.ErrorMessage,
		LastSeenAt:       event.CreatedAt,
		UpdatedAt:        time.Now(),
	}

	s.mu.Lock()
	s.cameras[cameraID] = status
	s.mu.Unlock()
}

func (s *MemoryStore) CameraStatuses() []CameraStatus {
	if s == nil {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]CameraStatus, 0, len(s.cameras))
	for _, status := range s.cameras {
		out = append(out, status)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GateNo == out[j].GateNo {
			return out[i].CameraID < out[j].CameraID
		}
		return out[i].GateNo < out[j].GateNo
	})
	return out
}

func (s *MemoryStore) AddPending(review LprPendingReview) LprPendingReview {
	if s == nil {
		return review
	}

	now := time.Now()
	if review.ID == "" {
		review.ID = uuid.NewString()
	}
	if review.Status == "" {
		review.Status = PendingStatusPending
	}
	if review.CreatedAt.IsZero() {
		review.CreatedAt = now
	}
	review.UpdatedAt = now

	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.pending[review.ID]; ok {
		return existing
	}
	if existing, ok := s.findDuplicatePendingLocked(review, now); ok {
		return existing
	}

	s.pending[review.ID] = review
	s.pendingOrder = append([]string{review.ID}, s.pendingOrder...)
	if len(s.pendingOrder) > s.capacity {
		oldest := s.pendingOrder[len(s.pendingOrder)-1]
		s.pendingOrder = s.pendingOrder[:len(s.pendingOrder)-1]
		delete(s.pending, oldest)
	}
	return review
}

func (s *MemoryStore) PendingReviews(limit int) []LprPendingReview {
	if s == nil {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 || limit > len(s.pendingOrder) {
		limit = len(s.pendingOrder)
	}
	out := make([]LprPendingReview, 0, limit)
	for _, id := range s.pendingOrder {
		if len(out) >= limit {
			break
		}
		if review, ok := s.pending[id]; ok {
			out = append(out, review)
		}
	}
	return out
}

func (s *MemoryStore) GetPending(id string) (LprPendingReview, bool) {
	if s == nil || id == "" {
		return LprPendingReview{}, false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	review, ok := s.pending[id]
	return review, ok
}

func (s *MemoryStore) FindPendingByEventID(eventID string) (LprPendingReview, bool) {
	if s == nil || eventID == "" {
		return LprPendingReview{}, false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, id := range s.pendingOrder {
		review, ok := s.pending[id]
		if ok && review.EventID == eventID && isOpenPendingStatus(review.Status) {
			return review, true
		}
	}
	return LprPendingReview{}, false
}

func (s *MemoryStore) UpdatePending(review LprPendingReview) bool {
	if s == nil || review.ID == "" {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.pending[review.ID]
	if !ok {
		return false
	}
	if review.CreatedAt.IsZero() {
		review.CreatedAt = existing.CreatedAt
	}
	review.UpdatedAt = time.Now()
	s.pending[review.ID] = review
	return true
}

func (s *MemoryStore) MutatePending(id string, operator string, version int, now time.Time, mutate func(*LprPendingReview) error) (LprPendingReview, error) {
	if s == nil || id == "" {
		return LprPendingReview{}, errPendingNotFound
	}
	if now.IsZero() {
		now = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	review, ok := s.pending[id]
	if !ok {
		return LprPendingReview{}, errPendingNotFound
	}
	if err := CheckPendingMutationAllowed(review, operator, version, now); err != nil {
		return review, err
	}
	beforeStatus := review.Status
	if IsLockExpired(review, now) {
		review.LockedBy = ""
		review.LockedUntil = nil
	}
	if mutate != nil {
		if err := mutate(&review); err != nil {
			return review, err
		}
	}
	if !CanTransition(beforeStatus, review.Status) {
		return review, ErrPendingTransition
	}
	review.Version++
	review.UpdatedAt = now
	if IsFinalPendingStatus(review.Status) {
		review.LockedBy = ""
		review.LockedUntil = nil
		if review.FinalizedAt == nil {
			finalizedAt := now
			review.FinalizedAt = &finalizedAt
		}
	}
	if review.CreatedAt.IsZero() {
		review.CreatedAt = now
	}
	s.pending[id] = review
	return review, nil
}

func (s *MemoryStore) ClaimPending(id string, operator string, ttl time.Duration, force bool, now time.Time) (LprPendingReview, error) {
	if s == nil || id == "" {
		return LprPendingReview{}, errPendingNotFound
	}
	if now.IsZero() {
		now = time.Now()
	}
	if ttl <= 0 {
		ttl = 30 * time.Second
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	review, ok := s.pending[id]
	if !ok {
		return LprPendingReview{}, errPendingNotFound
	}
	if IsFinalPendingStatus(review.Status) {
		return review, ErrPendingFinal
	}
	if review.LockedBy != "" && !IsLockExpired(review, now) && review.LockedBy != operator && !force {
		return review, ErrPendingLocked
	}

	lockedUntil := now.Add(ttl)
	review.LockedBy = operator
	review.LockedUntil = &lockedUntil
	review.Version++
	review.UpdatedAt = now
	s.pending[id] = review
	return review, nil
}

func (s *MemoryStore) ReleasePending(id string, operator string, force bool, now time.Time) (LprPendingReview, error) {
	if s == nil || id == "" {
		return LprPendingReview{}, errPendingNotFound
	}
	if now.IsZero() {
		now = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	review, ok := s.pending[id]
	if !ok {
		return LprPendingReview{}, errPendingNotFound
	}
	if IsFinalPendingStatus(review.Status) {
		return review, ErrPendingFinal
	}
	if review.LockedBy != "" && review.LockedBy != operator && !force {
		return review, ErrPendingLocked
	}

	review.LockedBy = ""
	review.LockedUntil = nil
	review.Version++
	review.UpdatedAt = now
	s.pending[id] = review
	return review, nil
}

func (s *MemoryStore) RejectPending(id string, reason string) bool {
	if s == nil || id == "" {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	review, ok := s.pending[id]
	if !ok {
		return false
	}
	review.Status = PendingStatusRejected
	if reason != "" {
		review.Reason = reason
	}
	review.UpdatedAt = time.Now()
	s.pending[id] = review
	return true
}

func (s *MemoryStore) AddManualCorrection(correction LprManualCorrection) {
	if s == nil {
		return
	}
	if correction.ID == "" {
		correction.ID = uuid.NewString()
	}
	if correction.CreatedAt.IsZero() {
		correction.CreatedAt = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.manualCorrections = append([]LprManualCorrection{correction}, s.manualCorrections...)
	if len(s.manualCorrections) > s.capacity {
		s.manualCorrections = s.manualCorrections[:s.capacity]
	}
}

func (s *MemoryStore) ManualCorrections(limit int) []LprManualCorrection {
	if s == nil {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 || limit > len(s.manualCorrections) {
		limit = len(s.manualCorrections)
	}
	out := make([]LprManualCorrection, limit)
	copy(out, s.manualCorrections[:limit])
	return out
}

func (s *MemoryStore) findDuplicatePendingLocked(review LprPendingReview, now time.Time) (LprPendingReview, bool) {
	reviewCamera := pendingCameraKey(review)
	for _, id := range s.pendingOrder {
		existing, ok := s.pending[id]
		if !ok || !isOpenPendingStatus(existing.Status) {
			continue
		}

		if review.EventID != "" && existing.EventID == review.EventID &&
			existing.GateNo == review.GateNo && pendingCameraKey(existing) == reviewCamera {
			return existing, true
		}

		if review.EventID == "" && existing.EventID == "" &&
			existing.GateNo == review.GateNo &&
			pendingCameraKey(existing) == reviewCamera &&
			existing.Reason == review.Reason &&
			existing.CreatedAt.After(now.Add(-pendingDedupeWindow)) {
			return existing, true
		}
	}
	return LprPendingReview{}, false
}

func cameraKey(event LprEvent) string {
	if event.CameraID != "" {
		return event.CameraID
	}
	if event.CameraIP != "" {
		return event.CameraIP
	}
	if event.GateNo != "" || event.Direction != "" || event.Endpoint != "" {
		return event.Endpoint + ":" + event.Direction + ":" + event.GateNo
	}
	return ""
}

func pendingCameraKey(review LprPendingReview) string {
	if review.CameraID != "" {
		return review.CameraID
	}
	return review.CameraIP
}

func isOpenPendingStatus(status string) bool {
	return status == "" || status == PendingStatusPending || status == PendingStatusSnapshotCaptured
}
