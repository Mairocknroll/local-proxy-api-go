package lpr

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"GO_LANG_WORKSPACE/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Broadcaster interface {
	Broadcast(group string, data []byte)
}

type Handler struct {
	store         *MemoryStore
	observer      *Observer
	snapshot      *CameraSnapshotService
	broadcaster   Broadcaster
	cloud         CustomerLookupClient
	parkingCode   string
	rescueEnabled bool
	rescueWindow  time.Duration
}

func NewHandler(store *MemoryStore, observer *Observer, snapshot *CameraSnapshotService, broadcaster Broadcaster, cloud CustomerLookupClient, parkingCode string, rescueEnabled bool, rescueWindow time.Duration) *Handler {
	if rescueWindow <= 0 {
		rescueWindow = 30 * time.Second
	}
	return &Handler{
		store:         store,
		observer:      observer,
		snapshot:      snapshot,
		broadcaster:   broadcaster,
		cloud:         cloud,
		parkingCode:   parkingCode,
		rescueEnabled: rescueEnabled,
		rescueWindow:  rescueWindow,
	}
}

func (h *Handler) RecentEvents(c *gin.Context) {
	success(c, h.store.RecentEvents(limitFromQuery(c)))
}

func (h *Handler) CameraStatuses(c *gin.Context) {
	success(c, h.store.CameraStatuses())
}

func (h *Handler) PendingReviews(c *gin.Context) {
	success(c, h.store.PendingReviews(limitFromQuery(c)))
}

func (h *Handler) ManualCorrections(c *gin.Context) {
	success(c, h.store.ManualCorrections(limitFromQuery(c)))
}

func (h *Handler) CaptureCameraSnapshot(c *gin.Context) {
	if !h.monitorEnabled() {
		monitorDisabled(c)
		return
	}
	if h.snapshot == nil {
		failure(c, http.StatusServiceUnavailable, "snapshot service not configured")
		return
	}

	var req snapshotRequest
	if !bindOptionalJSON(c, &req) {
		return
	}
	cameraID := c.Param("cameraId")
	pendingID := firstNonEmpty(c.Query("pending_id"), req.PendingID)
	gateNo := firstNonEmpty(c.Query("gate_no"), req.GateNo)
	direction := firstNonEmpty(c.Query("direction"), req.Direction)

	snapshotCameraID := cameraID
	result, err := h.snapshot.CaptureByCameraID(c.Request.Context(), cameraID)
	if err != nil && gateNo != "" {
		result, err = h.snapshot.CaptureByGate(c.Request.Context(), gateNo, direction)
		snapshotCameraID = result.CameraID
	}
	if err != nil {
		failure(c, http.StatusBadRequest, err.Error())
		return
	}

	var pending *LprPendingReview
	if pendingID != "" {
		review, err := h.upsertSnapshotPending(pendingID, nil, result, req.Operator, req.Version)
		if err != nil {
			h.pendingMutationFailure(c, err)
			return
		}
		pending = &review
	}
	event := h.recordSnapshotEvent(result, snapshotCameraID, gateNo, direction)
	h.broadcastSnapshot(result, pending, event)

	success(c, gin.H{
		"snapshot": result,
		"pending":  pending,
		"event":    event,
	})
}

func (h *Handler) CaptureEventSnapshot(c *gin.Context) {
	if !h.monitorEnabled() {
		monitorDisabled(c)
		return
	}
	if h.snapshot == nil {
		failure(c, http.StatusServiceUnavailable, "snapshot service not configured")
		return
	}

	eventID := c.Param("eventId")
	event, ok := h.store.GetEvent(eventID)
	if !ok {
		failure(c, http.StatusNotFound, "event not found")
		return
	}

	var req snapshotRequest
	if !bindOptionalJSON(c, &req) {
		return
	}
	pendingID := firstNonEmpty(c.Query("pending_id"), req.PendingID)

	result, err := h.snapshot.CaptureForEvent(c.Request.Context(), event)
	if err != nil {
		failure(c, http.StatusBadRequest, err.Error())
		return
	}

	var pending *LprPendingReview
	if pendingID != "" {
		review, err := h.upsertSnapshotPending(pendingID, &event, result, req.Operator, req.Version)
		if err != nil {
			h.pendingMutationFailure(c, err)
			return
		}
		pending = &review
	} else if review, ok := h.store.FindPendingByEventID(eventID); ok {
		updated, err := h.store.MutatePending(review.ID, req.Operator, req.Version, time.Now(), func(p *LprPendingReview) error {
			*p = updatePendingWithSnapshot(*p, result)
			return nil
		})
		if err != nil {
			h.pendingMutationFailure(c, err)
			return
		}
		pending = &updated
	}

	snapshotEvent := h.recordSnapshotEvent(result, event.CameraID, event.GateNo, event.Direction)
	h.broadcastSnapshot(result, pending, snapshotEvent)

	success(c, gin.H{
		"snapshot": result,
		"pending":  pending,
		"event":    snapshotEvent,
	})
}

func (h *Handler) ConfirmEvent(c *gin.Context) {
	if !h.monitorEnabled() {
		monitorDisabled(c)
		return
	}

	eventID := c.Param("eventId")
	var req confirmRequest
	if !bindJSON(c, &req) {
		return
	}
	req.CorrectedPlate = strings.TrimSpace(req.CorrectedPlate)
	if req.CorrectedPlate == "" {
		failure(c, http.StatusBadRequest, "corrected_plate is required")
		return
	}

	pending, ok := h.resolvePending(req.PendingID, eventID)
	if !ok {
		failure(c, http.StatusNotFound, "pending review not found")
		return
	}

	operator := firstNonEmpty(req.Operator, req.CorrectedBy)
	now := time.Now()
	pending, err := h.store.MutatePending(pending.ID, operator, req.Version, now, func(p *LprPendingReview) error {
		if p.Status == PendingStatusManualConfirmed {
			return ErrPendingTransition
		}
		p.Status = PendingStatusManualConfirmed
		p.CorrectedPlate = req.CorrectedPlate
		p.CorrectedBy = operator
		p.CorrectedAt = &now
		return nil
	})
	if err != nil {
		h.pendingMutationFailure(c, err)
		return
	}

	correction := LprManualCorrection{
		ID:             uuid.NewString(),
		PendingID:      pending.ID,
		EventID:        eventID,
		CorrectedPlate: req.CorrectedPlate,
		CorrectedBy:    operator,
		Note:           req.Note,
		CreatedAt:      now,
	}
	h.store.AddManualCorrection(correction)

	manualEvent := LprEvent{
		ID:          uuid.NewString(),
		UUID:        pending.UUID,
		Endpoint:    "lpr.manual-confirm",
		CameraID:    pending.CameraID,
		CameraIP:    pending.CameraIP,
		GateNo:      pending.GateNo,
		Direction:   pending.Direction,
		PlateText:   req.CorrectedPlate,
		EventStatus: StatusManualConfirmed,
		SourceType:  SourceManual,
		CreatedAt:   now,
	}
	h.store.AddEvent(manualEvent)
	h.store.UpdateCameraStatus(manualEvent)

	data := gin.H{
		"pending":    pending,
		"correction": correction,
		"event":      manualEvent,
	}
	h.broadcastMonitor(MonitorUpdateManualConfirmed, data)
	success(c, data)
}

func (h *Handler) RejectPending(c *gin.Context) {
	if !h.monitorEnabled() {
		monitorDisabled(c)
		return
	}

	pendingID := c.Param("pendingId")
	var req rejectRequest
	if !bindOptionalJSON(c, &req) {
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if req.RejectedBy != "" {
		if reason == "" {
			reason = "rejected_by=" + req.RejectedBy
		} else {
			reason = reason + " rejected_by=" + req.RejectedBy
		}
	}

	operator := firstNonEmpty(req.Operator, req.RejectedBy)
	pending, err := h.store.MutatePending(pendingID, operator, req.Version, time.Now(), func(p *LprPendingReview) error {
		p.Status = PendingStatusRejected
		if reason != "" {
			p.Reason = reason
		}
		return nil
	})
	if err != nil {
		h.pendingMutationFailure(c, err)
		return
	}
	h.broadcastMonitor(MonitorUpdatePendingRejected, pending)
	success(c, pending)
}

func (h *Handler) ClaimPending(c *gin.Context) {
	if !h.monitorEnabled() {
		monitorDisabled(c)
		return
	}

	var req ClaimPendingRequest
	if !bindJSON(c, &req) {
		return
	}
	req.Operator = strings.TrimSpace(req.Operator)
	if req.Operator == "" {
		failure(c, http.StatusBadRequest, "operator is required")
		return
	}
	ttl := time.Duration(req.TTLSeconds) * time.Second
	pending, err := h.store.ClaimPending(c.Param("pendingId"), req.Operator, ttl, req.Force, time.Now())
	if err != nil {
		h.pendingMutationFailure(c, err)
		return
	}
	h.broadcastMonitor(MonitorUpdatePendingUpdated, pending)
	success(c, pending)
}

func (h *Handler) ReleasePending(c *gin.Context) {
	if !h.monitorEnabled() {
		monitorDisabled(c)
		return
	}

	var req ReleasePendingRequest
	if !bindJSON(c, &req) {
		return
	}
	req.Operator = strings.TrimSpace(req.Operator)
	if req.Operator == "" && !req.Force {
		failure(c, http.StatusBadRequest, "operator is required")
		return
	}
	pending, err := h.store.ReleasePending(c.Param("pendingId"), req.Operator, req.Force, time.Now())
	if err != nil {
		h.pendingMutationFailure(c, err)
		return
	}
	h.broadcastMonitor(MonitorUpdatePendingUpdated, pending)
	success(c, pending)
}

func (h *Handler) RescueToKiosk(c *gin.Context) {
	if !h.monitorEnabled() || !h.rescueEnabled {
		c.JSON(http.StatusOK, gin.H{
			"status":  false,
			"message": "lpr rescue to kiosk disabled",
			"data":    nil,
		})
		return
	}
	if h.cloud == nil {
		failure(c, http.StatusServiceUnavailable, "cloud client not configured")
		return
	}

	pendingID := c.Param("pendingId")
	var req RescueToKioskRequest
	if !bindJSON(c, &req) {
		return
	}
	req.CorrectedPlate = strings.TrimSpace(req.CorrectedPlate)
	if req.CorrectedPlate == "" {
		failure(c, http.StatusBadRequest, "corrected_plate is required")
		return
	}

	pending, ok := h.store.GetPending(pendingID)
	if !ok {
		failure(c, http.StatusNotFound, "pending review not found")
		return
	}
	now := time.Now()
	if err := CheckPendingMutationAllowed(pending, req.Operator, req.Version, now); err != nil {
		h.pendingMutationFailure(c, err)
		return
	}
	if !CanTransition(pending.Status, PendingStatusRescuedToKiosk) {
		failure(c, http.StatusConflict, "pending review status cannot be rescued")
		return
	}
	if pending.GateNo == "" {
		failure(c, http.StatusBadRequest, "pending review gate_no is required")
		return
	}
	if h.rescueWindow > 0 && !pending.CreatedAt.IsZero() && now.Sub(pending.CreatedAt) > h.rescueWindow {
		pending, err := h.store.MutatePending(pending.ID, req.Operator, req.Version, now, func(p *LprPendingReview) error {
			p.Status = PendingStatusRescueExpired
			p.CorrectedPlate = req.CorrectedPlate
			p.CorrectedBy = req.Operator
			return nil
		})
		if err != nil {
			h.pendingMutationFailure(c, err)
			return
		}
		h.addRescueCorrection(pending, req)
		h.broadcastMonitor(MonitorUpdateRescueExpired, pending)
		failure(c, http.StatusBadRequest, "pending review rescue window expired")
		return
	}

	cloudRes, err := h.cloud.GetCustomerID(c.Request.Context(), req.CorrectedPlate, h.parkingCode)
	if err != nil {
		pending, mutateErr := h.store.MutatePending(pending.ID, req.Operator, req.Version, time.Now(), func(p *LprPendingReview) error {
			p.Status = PendingStatusRescueFailed
			p.CorrectedPlate = req.CorrectedPlate
			p.CorrectedBy = req.Operator
			return nil
		})
		if mutateErr != nil {
			h.pendingMutationFailure(c, mutateErr)
			return
		}
		h.addRescueCorrection(pending, req)
		h.broadcastMonitor(MonitorUpdateRescueFailed, gin.H{"pending": pending, "error": err.Error()})
		failure(c, http.StatusBadGateway, err.Error())
		return
	}

	custID, efID, rescuable := entitlementFromCloud(cloudRes)
	resp := RescueToKioskResponse{
		PendingID:      pending.ID,
		EventID:        pending.EventID,
		GateNo:         pending.GateNo,
		CorrectedPlate: req.CorrectedPlate,
		CustID:         custID,
		EFID:           efID,
		Rescued:        false,
	}

	if !rescuable {
		pending, err := h.store.MutatePending(pending.ID, req.Operator, req.Version, time.Now(), func(p *LprPendingReview) error {
			p.Status = PendingStatusValidatedNotMember
			p.CorrectedPlate = req.CorrectedPlate
			p.CorrectedBy = req.Operator
			return nil
		})
		if err != nil {
			h.pendingMutationFailure(c, err)
			return
		}
		correction := h.addRescueCorrection(pending, req)
		resp.Reason = "validated_not_member"
		h.broadcastMonitor(MonitorUpdateValidatedNotMember, gin.H{
			"pending":    pending,
			"correction": correction,
			"response":   resp,
		})
		success(c, resp)
		return
	}

	rescueNow := time.Now()
	rescueID := rescueUUID(pending, rescueNow)
	timeIn := rescueTimeIn(pending, rescueNow)
	pending, err = h.store.MutatePending(pending.ID, req.Operator, req.Version, rescueNow, func(p *LprPendingReview) error {
		p.Status = PendingStatusRescuedToKiosk
		p.CorrectedPlate = req.CorrectedPlate
		p.CorrectedBy = req.Operator
		p.CorrectedAt = &rescueNow
		return nil
	})
	if err != nil {
		h.pendingMutationFailure(c, err)
		return
	}
	correction := h.addRescueCorrection(pending, req)

	room := "gate_in_" + pending.GateNo
	payload := h.buildRescueKioskPayload(pending, req, rescueID, timeIn, custID, efID)
	if h.broadcaster != nil {
		if b, err := json.Marshal(payload); err == nil {
			h.broadcaster.Broadcast(room, b)
		}
	}

	rescueEvent := LprEvent{
		ID:          uuid.NewString(),
		UUID:        rescueID,
		Endpoint:    "lpr.rescue-to-kiosk",
		CameraID:    pending.CameraID,
		CameraIP:    pending.CameraIP,
		GateNo:      pending.GateNo,
		Direction:   pending.Direction,
		PlateText:   req.CorrectedPlate,
		EventStatus: StatusRescuedToKiosk,
		VehicleType: rescueVehicleType(req, pending),
		SourceType:  SourceManualRescue,
		CreatedAt:   rescueNow,
	}
	h.store.AddEvent(rescueEvent)
	h.store.UpdateCameraStatus(rescueEvent)

	resp.Rescued = true
	resp.BroadcastRoom = room
	h.broadcastMonitor(MonitorUpdateRescuedToKiosk, gin.H{
		"pending":    pending,
		"correction": correction,
		"event":      rescueEvent,
		"response":   resp,
	})
	success(c, resp)
}

func (h *Handler) upsertSnapshotPending(pendingID string, event *LprEvent, result SnapshotResult, operator string, version int) (LprPendingReview, error) {
	if existing, ok := h.store.GetPending(pendingID); ok {
		return h.store.MutatePending(existing.ID, operator, version, time.Now(), func(p *LprPendingReview) error {
			*p = updatePendingWithSnapshot(*p, result)
			return nil
		})
	}

	review := LprPendingReview{
		ID:           pendingID,
		Status:       PendingStatusSnapshotCaptured,
		SnapshotURL:  result.ImageURL,
		SnapshotPath: result.ImagePath,
		CameraID:     result.CameraID,
		GateNo:       result.GateNo,
		Reason:       string(StatusSnapshotCaptured),
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if event != nil {
		review.EventID = event.ID
		review.UUID = event.UUID
		review.CameraID = firstNonEmpty(event.CameraID, result.CameraID)
		review.CameraIP = event.CameraIP
		review.GateNo = firstNonEmpty(event.GateNo, result.GateNo)
		review.Direction = event.Direction
		review.PlateText = event.PlateText
	}
	return h.store.AddPending(review), nil
}

func (h *Handler) recordSnapshotEvent(result SnapshotResult, cameraID, gateNo, direction string) LprEvent {
	now := time.Now()
	event := LprEvent{
		ID:          uuid.NewString(),
		Endpoint:    "lpr.snapshot",
		CameraID:    firstNonEmpty(cameraID, result.CameraID),
		GateNo:      firstNonEmpty(gateNo, result.GateNo),
		Direction:   direction,
		EventStatus: StatusSnapshotCaptured,
		ImageURL:    result.ImageURL,
		SourceType:  SourceSnapshot,
		CreatedAt:   now,
	}
	h.store.AddEvent(event)
	h.store.UpdateCameraStatus(event)
	return event
}

func (h *Handler) resolvePending(pendingID, eventID string) (LprPendingReview, bool) {
	if pendingID != "" {
		return h.store.GetPending(pendingID)
	}
	return h.store.FindPendingByEventID(eventID)
}

func (h *Handler) broadcastSnapshot(result SnapshotResult, pending *LprPendingReview, event LprEvent) {
	h.broadcastMonitor(MonitorUpdateSnapshotCaptured, gin.H{
		"snapshot": result,
		"pending":  pending,
		"event":    event,
	})
	if pending != nil {
		h.broadcastMonitor(MonitorUpdatePendingUpdated, *pending)
	}
}

func (h *Handler) broadcastMonitor(updateType string, data any) {
	if h.observer == nil {
		return
	}
	h.observer.BroadcastMonitorUpdate(updateType, data)
}

func (h *Handler) buildRescueKioskPayload(pending LprPendingReview, req RescueToKioskRequest, rescueID string, timeIn string, custID, efID any) map[string]any {
	return map[string]any{
		"license_plate":            req.CorrectedPlate,
		"uuid":                     rescueID,
		"time_in":                  timeIn,
		"cust_id":                  custID,
		"ef_id":                    efID,
		"vehicle_type":             utils.VehicleType(rescueVehicleType(req, pending)),
		"license_plate_img_base64": rescueImageBase64(pending),
	}
}

func (h *Handler) addRescueCorrection(pending LprPendingReview, req RescueToKioskRequest) LprManualCorrection {
	correction := LprManualCorrection{
		ID:             uuid.NewString(),
		PendingID:      pending.ID,
		EventID:        pending.EventID,
		CorrectedPlate: req.CorrectedPlate,
		CorrectedBy:    req.Operator,
		Note:           req.Note,
		CreatedAt:      time.Now(),
	}
	h.store.AddManualCorrection(correction)
	return correction
}

func (h *Handler) monitorEnabled() bool {
	return h.observer == nil || h.observer.Enabled()
}

func updatePendingWithSnapshot(review LprPendingReview, result SnapshotResult) LprPendingReview {
	review.Status = PendingStatusSnapshotCaptured
	review.SnapshotURL = result.ImageURL
	review.SnapshotPath = result.ImagePath
	review.UpdatedAt = time.Now()
	return review
}

func limitFromQuery(c *gin.Context) int {
	limit := 50
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = n
		}
	}
	if limit <= 0 {
		return 50
	}
	if limit > 500 {
		return 500
	}
	return limit
}

func bindJSON(c *gin.Context, out any) bool {
	if err := c.ShouldBindJSON(out); err != nil {
		failure(c, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

func bindOptionalJSON(c *gin.Context, out any) bool {
	if !strings.Contains(strings.ToLower(c.GetHeader("Content-Type")), "application/json") {
		return true
	}
	if err := c.ShouldBindJSON(out); err != nil && err != io.EOF {
		failure(c, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

func success(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{
		"status":  true,
		"message": "success",
		"data":    data,
	})
}

func failure(c *gin.Context, code int, message string) {
	c.JSON(code, gin.H{
		"status":  false,
		"message": message,
		"data":    nil,
	})
}

func (h *Handler) pendingMutationFailure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, errPendingNotFound):
		failure(c, http.StatusNotFound, "pending review not found")
	case errors.Is(err, ErrPendingFinal),
		errors.Is(err, ErrPendingLocked),
		errors.Is(err, ErrPendingVersionConflict),
		errors.Is(err, ErrPendingTransition):
		failure(c, http.StatusConflict, err.Error())
	default:
		failure(c, http.StatusBadRequest, err.Error())
	}
}

func monitorDisabled(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  false,
		"message": "lpr monitor disabled",
		"data":    nil,
	})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func rescueUUID(pending LprPendingReview, now time.Time) string {
	if pending.UUID != "" {
		return pending.UUID
	}
	if pending.EventID != "" {
		return pending.EventID
	}
	gateNo := pending.GateNo
	if gateNo == "" {
		gateNo = "unknown"
	}
	if now.IsZero() {
		now = time.Now()
	}
	return "manual-rescue-" + gateNo + "-" + strconv.FormatInt(now.UnixNano(), 10)
}

func rescueTimeIn(pending LprPendingReview, now time.Time) string {
	if !pending.CreatedAt.IsZero() {
		return pending.CreatedAt.Format(time.RFC3339)
	}
	if now.IsZero() {
		now = time.Now()
	}
	return now.Format(time.RFC3339)
}

func rescueVehicleType(req RescueToKioskRequest, pending LprPendingReview) string {
	if strings.TrimSpace(req.VehicleType) != "" {
		return req.VehicleType
	}
	if strings.TrimSpace(pending.VehicleType) != "" {
		return pending.VehicleType
	}
	return "car"
}

func rescueImageBase64(pending LprPendingReview) string {
	if strings.TrimSpace(pending.SnapshotPath) == "" {
		return ""
	}
	b, err := os.ReadFile(pending.SnapshotPath)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}

func entitlementFromCloud(res map[string]any) (custID, efID any, ok bool) {
	if res == nil {
		return nil, nil, false
	}
	custID = res["cust_id"]
	efID = res["ef_id"]
	if data, isMap := res["data"].(map[string]any); isMap {
		if !hasValue(custID) {
			custID = data["cust_id"]
		}
		if !hasValue(efID) {
			efID = data["ef_id"]
		}
	}
	return custID, efID, hasValue(custID) || hasValue(efID)
}

func hasValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(x) != ""
	default:
		return true
	}
}

type snapshotRequest struct {
	PendingID string `json:"pending_id"`
	GateNo    string `json:"gate_no"`
	Direction string `json:"direction"`
	Operator  string `json:"operator"`
	Version   int    `json:"version"`
}

type confirmRequest struct {
	PendingID      string `json:"pending_id"`
	CorrectedPlate string `json:"corrected_plate"`
	Operator       string `json:"operator"`
	Version        int    `json:"version"`
	CorrectedBy    string `json:"corrected_by"`
	Note           string `json:"note"`
}

type rejectRequest struct {
	Reason     string `json:"reason"`
	Operator   string `json:"operator"`
	Version    int    `json:"version"`
	RejectedBy string `json:"rejected_by"`
}
