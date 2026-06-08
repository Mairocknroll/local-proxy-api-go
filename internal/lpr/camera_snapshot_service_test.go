package lpr

import (
	"context"
	"testing"

	"GO_LANG_WORKSPACE/internal/config"
)

func TestSnapshotServiceMissingCameraMapping(t *testing.T) {
	service := NewCameraSnapshotService(&config.Config{})

	_, err := service.CaptureByCameraID(context.Background(), "missing-camera-for-test")
	if err == nil {
		t.Fatal("CaptureByCameraID error = nil, want mapping error")
	}
}
