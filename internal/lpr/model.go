package lpr

import "time"

type EventStatus string

const (
	StatusReadOK             EventStatus = "READ_OK"
	StatusNoPlate            EventStatus = "NO_PLATE"
	StatusUnknownPlate       EventStatus = "UNKNOWN_PLATE"
	StatusParseError         EventStatus = "PARSE_ERROR"
	StatusMultipartError     EventStatus = "MULTIPART_ERROR"
	StatusUpstreamError      EventStatus = "UPSTREAM_ERROR"
	StatusDuplicate          EventStatus = "DUPLICATE"
	StatusAmbiguous          EventStatus = "AMBIGUOUS"
	StatusSnapshotCaptured   EventStatus = "SNAPSHOT_CAPTURED"
	StatusManualConfirmed    EventStatus = "MANUAL_CONFIRMED"
	StatusRescuedToKiosk     EventStatus = "RESCUED_TO_KIOSK"
	StatusValidatedNotMember EventStatus = "VALIDATED_NOT_MEMBER"
	StatusRescueExpired      EventStatus = "RESCUE_EXPIRED"
	StatusRescueFailed       EventStatus = "RESCUE_FAILED"
)

type SourceType string

const (
	SourceLPRHook      SourceType = "LPR_HOOK"
	SourceSnapshot     SourceType = "SNAPSHOT"
	SourceManual       SourceType = "MANUAL"
	SourceManualRescue SourceType = "MANUAL_RESCUE"
)

const (
	MonitorGroup = "lpr_monitor"
)

const (
	PendingStatusPending            = "PENDING"
	PendingStatusSnapshotCaptured   = "SNAPSHOT_CAPTURED"
	PendingStatusManualConfirmed    = "MANUAL_CONFIRMED"
	PendingStatusRejected           = "REJECTED"
	PendingStatusExpired            = "EXPIRED"
	PendingStatusRescuedToKiosk     = "RESCUED_TO_KIOSK"
	PendingStatusValidatedNotMember = "VALIDATED_NOT_MEMBER"
	PendingStatusRescueExpired      = "RESCUE_EXPIRED"
	PendingStatusRescueFailed       = "RESCUE_FAILED"
)

const (
	MonitorUpdatePendingCreated     = "PENDING_REVIEW_CREATED"
	MonitorUpdatePendingUpdated     = "PENDING_REVIEW_UPDATED"
	MonitorUpdateSnapshotCaptured   = "SNAPSHOT_CAPTURED"
	MonitorUpdateManualConfirmed    = "MANUAL_CONFIRMED"
	MonitorUpdatePendingRejected    = "PENDING_REVIEW_REJECTED"
	MonitorUpdateRescuedToKiosk     = "RESCUED_TO_KIOSK"
	MonitorUpdateValidatedNotMember = "VALIDATED_NOT_MEMBER"
	MonitorUpdateRescueExpired      = "RESCUE_EXPIRED"
	MonitorUpdateRescueFailed       = "RESCUE_FAILED"
)

type LprEvent struct {
	ID                string      `json:"id"`
	UUID              string      `json:"uuid,omitempty"`
	RequestID         string      `json:"request_id,omitempty"`
	Endpoint          string      `json:"endpoint,omitempty"`
	CameraID          string      `json:"camera_id,omitempty"`
	CameraIP          string      `json:"camera_ip,omitempty"`
	GateNo            string      `json:"gate_no,omitempty"`
	Direction         string      `json:"direction,omitempty"`
	PlateText         string      `json:"plate_text,omitempty"`
	RawPlateText      string      `json:"raw_plate_text,omitempty"`
	Confidence        *float64    `json:"confidence,omitempty"`
	EventStatus       EventStatus `json:"event_status"`
	VehicleType       string      `json:"vehicle_type,omitempty"`
	ImageURL          string      `json:"image_url,omitempty"`
	CropImageURL      string      `json:"crop_image_url,omitempty"`
	RawPayload        string      `json:"raw_payload,omitempty"`
	SourceType        SourceType  `json:"source_type"`
	AssignedSessionID string      `json:"assigned_session_id,omitempty"`
	ErrorMessage      string      `json:"error_message,omitempty"`
	CreatedAt         time.Time   `json:"created_at"`
}

type CameraStatus struct {
	CameraID         string      `json:"camera_id"`
	CameraIP         string      `json:"camera_ip,omitempty"`
	GateNo           string      `json:"gate_no,omitempty"`
	Direction        string      `json:"direction,omitempty"`
	HealthStatus     string      `json:"health_status"`
	LastEventID      string      `json:"last_event_id,omitempty"`
	LastEventStatus  EventStatus `json:"last_event_status,omitempty"`
	LastPlateText    string      `json:"last_plate_text,omitempty"`
	LastErrorMessage string      `json:"last_error_message,omitempty"`
	LastSeenAt       time.Time   `json:"last_seen_at"`
	UpdatedAt        time.Time   `json:"updated_at"`
}

type LprPendingReview struct {
	ID             string     `json:"id"`
	EventID        string     `json:"event_id,omitempty"`
	UUID           string     `json:"uuid,omitempty"`
	CameraID       string     `json:"camera_id,omitempty"`
	CameraIP       string     `json:"camera_ip,omitempty"`
	GateNo         string     `json:"gate_no,omitempty"`
	Direction      string     `json:"direction,omitempty"`
	PlateText      string     `json:"plate_text,omitempty"`
	VehicleType    string     `json:"vehicle_type,omitempty"`
	Reason         string     `json:"reason,omitempty"`
	Status         string     `json:"status"`
	SnapshotURL    string     `json:"snapshot_url,omitempty"`
	SnapshotPath   string     `json:"snapshot_path,omitempty"`
	CorrectedPlate string     `json:"corrected_plate,omitempty"`
	CorrectedBy    string     `json:"corrected_by,omitempty"`
	CorrectedAt    *time.Time `json:"corrected_at,omitempty"`
	Version        int        `json:"version"`
	LockedBy       string     `json:"locked_by,omitempty"`
	LockedUntil    *time.Time `json:"locked_until,omitempty"`
	FinalizedAt    *time.Time `json:"finalized_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type LprManualCorrection struct {
	ID             string    `json:"id"`
	PendingID      string    `json:"pending_id,omitempty"`
	EventID        string    `json:"event_id,omitempty"`
	CorrectedPlate string    `json:"corrected_plate"`
	CorrectedBy    string    `json:"corrected_by,omitempty"`
	Note           string    `json:"note,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type SnapshotResult struct {
	CameraID   string    `json:"camera_id,omitempty"`
	GateNo     string    `json:"gate_no,omitempty"`
	ImagePath  string    `json:"image_path,omitempty"`
	ImageURL   string    `json:"image_url,omitempty"`
	CapturedAt time.Time `json:"captured_at"`
}

type MonitorUpdate struct {
	Type      string    `json:"type"`
	Data      any       `json:"data"`
	CreatedAt time.Time `json:"created_at"`
}

type RescueToKioskRequest struct {
	CorrectedPlate string `json:"corrected_plate"`
	Operator       string `json:"operator"`
	Version        int    `json:"version"`
	Note           string `json:"note"`
	VehicleType    string `json:"vehicle_type"`
	Force          bool   `json:"force"`
}

type RescueToKioskResponse struct {
	PendingID      string `json:"pending_id"`
	EventID        string `json:"event_id,omitempty"`
	GateNo         string `json:"gate_no,omitempty"`
	CorrectedPlate string `json:"corrected_plate"`
	CustID         any    `json:"cust_id,omitempty"`
	EFID           any    `json:"ef_id,omitempty"`
	Rescued        bool   `json:"rescued"`
	Reason         string `json:"reason,omitempty"`
	BroadcastRoom  string `json:"broadcast_room,omitempty"`
}

type ClaimPendingRequest struct {
	Operator   string `json:"operator"`
	TTLSeconds int    `json:"ttl_seconds"`
	Force      bool   `json:"force"`
}

type ReleasePendingRequest struct {
	Operator string `json:"operator"`
	Force    bool   `json:"force"`
}
