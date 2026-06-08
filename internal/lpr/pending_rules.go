package lpr

import (
	"errors"
	"fmt"
	"time"
)

var (
	ErrPendingFinal           = errors.New("pending review is final")
	ErrPendingLocked          = errors.New("pending review is locked by another operator")
	ErrPendingVersionConflict = errors.New("pending review version conflict")
	ErrPendingTransition      = errors.New("pending review status transition not allowed")
)

func IsFinalPendingStatus(status string) bool {
	switch status {
	case PendingStatusRescuedToKiosk, PendingStatusRejected, PendingStatusExpired, PendingStatusRescueExpired:
		return true
	default:
		return false
	}
}

func IsLockExpired(pending LprPendingReview, now time.Time) bool {
	return pending.LockedUntil == nil || !pending.LockedUntil.After(now)
}

func CheckPendingMutationAllowed(pending LprPendingReview, operator string, version int, now time.Time) error {
	if IsFinalPendingStatus(pending.Status) {
		return fmt.Errorf("%w: %s", ErrPendingFinal, pending.Status)
	}
	if version != pending.Version {
		return fmt.Errorf("%w: current=%d requested=%d", ErrPendingVersionConflict, pending.Version, version)
	}
	if pending.LockedBy != "" && !IsLockExpired(pending, now) && pending.LockedBy != operator {
		return fmt.Errorf("%w: %s", ErrPendingLocked, pending.LockedBy)
	}
	return nil
}

func CanTransition(from string, to string) bool {
	if IsFinalPendingStatus(from) {
		return false
	}
	if from == to {
		return true
	}
	if from == PendingStatusManualConfirmed {
		return false
	}
	switch to {
	case PendingStatusPending,
		PendingStatusSnapshotCaptured,
		PendingStatusManualConfirmed,
		PendingStatusRejected,
		PendingStatusExpired,
		PendingStatusRescuedToKiosk,
		PendingStatusValidatedNotMember,
		PendingStatusRescueExpired,
		PendingStatusRescueFailed:
		return true
	default:
		return false
	}
}
