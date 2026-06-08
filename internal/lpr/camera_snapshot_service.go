package lpr

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"GO_LANG_WORKSPACE/internal/config"
	"GO_LANG_WORKSPACE/internal/utils"
)

type CameraSnapshotService struct {
	cfg *config.Config
}

type ResolvedCamera struct {
	CameraID  string
	CameraIP  string
	GateNo    string
	Direction string
}

func NewCameraSnapshotService(cfg *config.Config) *CameraSnapshotService {
	return &CameraSnapshotService{cfg: cfg}
}

func (s *CameraSnapshotService) CaptureByCameraID(ctx context.Context, cameraID string) (SnapshotResult, error) {
	camera, err := s.ResolveCameraByID(cameraID)
	if err != nil {
		return SnapshotResult{}, err
	}
	return s.captureFromHost(ctx, camera)
}

func (s *CameraSnapshotService) CaptureByGate(ctx context.Context, gateNo, direction string) (SnapshotResult, error) {
	camera, err := s.ResolveCameraByGate(gateNo, direction)
	if err != nil {
		return SnapshotResult{}, err
	}
	return s.captureFromHost(ctx, camera)
}

func (s *CameraSnapshotService) CaptureForEvent(ctx context.Context, event LprEvent) (SnapshotResult, error) {
	if strings.TrimSpace(event.CameraID) != "" {
		return s.CaptureByCameraID(ctx, event.CameraID)
	}
	return s.CaptureByGate(ctx, event.GateNo, event.Direction)
}

func (s *CameraSnapshotService) ResolveCameraByID(cameraID string) (ResolvedCamera, error) {
	cameraID = strings.TrimSpace(cameraID)
	if cameraID == "" {
		return ResolvedCamera{}, fmt.Errorf("camera_id is required")
	}

	if looksLikeHost(cameraID) {
		return ResolvedCamera{CameraID: cameraID, CameraIP: cameraID}, nil
	}

	for _, key := range cameraEnvCandidates(cameraID) {
		if host := strings.TrimSpace(os.Getenv(key)); host != "" {
			return ResolvedCamera{CameraID: cameraID, CameraIP: host}, nil
		}
	}

	return ResolvedCamera{}, fmt.Errorf("camera mapping not found for camera_id=%s; configure LPR_CAMERA_%s or an existing camera env key", cameraID, normalizeEnvKey(cameraID))
}

func (s *CameraSnapshotService) ResolveCameraByGate(gateNo, direction string) (ResolvedCamera, error) {
	gateNo = strings.TrimSpace(gateNo)
	if gateNo == "" {
		return ResolvedCamera{}, fmt.Errorf("gate_no is required")
	}

	padded := fmt.Sprintf("%02s", gateNo)
	direction = strings.ToUpper(strings.TrimSpace(direction))
	var envKeys []string
	switch direction {
	case "ENT":
		envKeys = []string{"LIC_IN_" + padded, "LPR_IN_" + padded}
	case "EXT":
		envKeys = []string{"LIC_OUT_" + padded, "LPR_OUT_" + padded}
	default:
		envKeys = []string{"LIC_IN_" + padded, "LPR_IN_" + padded, "LIC_OUT_" + padded, "LPR_OUT_" + padded}
	}

	for _, key := range envKeys {
		if host := strings.TrimSpace(os.Getenv(key)); host != "" {
			return ResolvedCamera{
				CameraID:  key,
				CameraIP:  host,
				GateNo:    gateNo,
				Direction: direction,
			}, nil
		}
	}
	return ResolvedCamera{}, fmt.Errorf("camera mapping not found for gate_no=%s direction=%s", gateNo, direction)
}

func (s *CameraSnapshotService) captureFromHost(ctx context.Context, camera ResolvedCamera) (SnapshotResult, error) {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return SnapshotResult{}, ctx.Err()
		default:
		}
	}

	b64, err := utils.FetchLprSnapshotFromHost(s.cfg, camera.CameraIP)
	if err != nil {
		return SnapshotResult{}, err
	}
	return saveSnapshot(camera.CameraID, camera.GateNo, b64)
}

func saveSnapshot(cameraID, gateNo, b64 string) (SnapshotResult, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("decode snapshot: %w", err)
	}

	now := time.Now()
	dir := filepath.Join("snapshots", "lpr-monitor", now.Format("20060102"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return SnapshotResult{}, fmt.Errorf("create snapshot directory: %w", err)
	}

	name := fmt.Sprintf("%s_%s.jpg", safeFilePart(cameraID), now.Format("150405.000000000"))
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return SnapshotResult{}, fmt.Errorf("write snapshot: %w", err)
	}

	return SnapshotResult{
		CameraID:   cameraID,
		GateNo:     gateNo,
		ImagePath:  path,
		ImageURL:   "",
		CapturedAt: now,
	}, nil
}

func cameraEnvCandidates(cameraID string) []string {
	upper := strings.ToUpper(strings.TrimSpace(cameraID))
	normalized := normalizeEnvKey(cameraID)
	return []string{
		upper,
		"LPR_CAMERA_" + normalized,
		"CAMERA_" + normalized,
		normalized,
	}
}

func normalizeEnvKey(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	re := regexp.MustCompile(`[^A-Z0-9]+`)
	s = re.ReplaceAllString(s, "_")
	return strings.Trim(s, "_")
}

func safeFilePart(s string) string {
	if strings.TrimSpace(s) == "" {
		s = "camera"
	}
	s = normalizeEnvKey(s)
	if s == "" {
		return "camera"
	}
	return strings.ToLower(s)
}

func looksLikeHost(s string) bool {
	return strings.Contains(s, ".") || strings.Contains(s, ":")
}
