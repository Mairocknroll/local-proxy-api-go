package utils

import (
	"GO_LANG_WORKSPACE/internal/config"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	digest "github.com/icholy/digest"
)

// ----------------------------------------------------
// Const & minimal ENV
// ----------------------------------------------------

const (
	minUsefulBytes     = 800
	snapshotDir        = "./snapshots"
	snapshotScheme     = "http" // ใช้ http ตายตัว; ถ้าจำเป็นค่อยเปลี่ยนเป็น https
	lprSnapshotPath    = "/ISAPI/Streaming/channels/1/picture"
	driverSnapshotPath = "/ISAPI/Streaming/channels/2/picture"
)

func getenvInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func getenvBool(k string, def bool) bool {
	if v := os.Getenv(k); v != "" {
		switch strings.ToLower(v) {
		case "1", "true", "yes", "y", "on":
			return true
		case "0", "false", "no", "n", "off":
			return false
		}
	}
	return def
}

var (
	// ขยายเวลาเป็น 5s ตามที่เห็นจาก Postman หน้างาน (~3.5s)
	snapshotTimeout         = time.Duration(getenvInt("SNAPSHOT_TIMEOUT_MS", 5000)) * time.Millisecond
	insecureSkipVerifyHTTPS = getenvBool("SNAPSHOT_INSECURE_SKIP_VERIFY", false)
)

// fallback paths (ยังคงไว้สำหรับ FetchImagesConcurrently/กรณี debug)
var candidatePaths = []string{
	"/ISAPI/Streaming/channels/1/picture",
	"/ISAPI/Streaming/channels/101/picture",
	"/ISAPI/Streaming/channels/102/picture",
	"/ISAPI/Streaming/channels/201/picture",
	"/ISAPI/Streaming/channels/202/picture",
	"/ISAPI/Streaming/channels/101/picture?snapShotImageType=JPEG",
	"/Streaming/channels/1/picture",
	"/Streaming/channels/101/picture",
	"/cgi-bin/snapshot.cgi",
	"/cgi-bin/snapshot.cgi?channel=1",
	"/axis-cgi/jpg/image.cgi",
	"/ISAPI/ContentMgmt/picture",
}

// ----------------------------------------------------
// Helpers
// ----------------------------------------------------

func pickHost(hosts map[string]string, prefs []string) (key, host string) {
	if len(hosts) == 0 {
		return "", ""
	}
	keys := make([]string, 0, len(hosts))
	for k := range hosts {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, p := range prefs {
		lp := strings.ToLower(p)
		for _, k := range keys {
			if strings.HasPrefix(strings.ToLower(k), lp) {
				if v := strings.TrimSpace(hosts[k]); v != "" {
					return k, v
				}
			}
		}
	}
	for _, k := range keys {
		if v := strings.TrimSpace(hosts[k]); v != "" {
			return k, v
		}
	}
	return "", ""
}

func digestClient(user, pass string) *http.Client {
	tp := &digest.Transport{
		Username: user,
		Password: pass,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: insecureSkipVerifyHTTPS},
		},
	}
	return &http.Client{Transport: tp, Timeout: snapshotTimeout}
}

func fetchDigest(url, user, pass string) ([]byte, int, error) {
	client := digestClient(user, pass)

	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Accept", "image/jpeg")

	ctx, cancel := context.WithTimeout(context.Background(), snapshotTimeout)
	defer cancel()
	req = req.WithContext(ctx)

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, resp.StatusCode, fmt.Errorf("status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	return b, resp.StatusCode, err
}

// ยิงพาธ “คงที่” ด้วย Digest เท่านั้น (ตัด Basic ทิ้ง)
func tryFetchExact(host, path, user, pass string) (string, int, error) {
	url := snapshotScheme + "://" + host + path
	if b, st, err := fetchDigest(url, user, pass); err == nil && st == 200 && len(b) > minUsefulBytes {
		return base64.StdEncoding.EncodeToString(b), st, nil
	} else {
		return "", st, err
	}
}

// ลองหลาย path แต่ “Digest only”
func tryFetchAll(host, user, pass string) (string, int, error) {
	var lastErr error
	var lastStatus int

	for _, p := range candidatePaths {
		url := snapshotScheme + "://" + host + p
		if b, st, err := fetchDigest(url, user, pass); err == nil && st == 200 && len(b) > minUsefulBytes {
			return base64.StdEncoding.EncodeToString(b), st, nil
		} else {
			lastErr, lastStatus = err, st
		}
	}
	return "", lastStatus, lastErr
}

func maskPass(p string) string {
	if p == "" {
		return ""
	}
	if len(p) <= 2 {
		return "**"
	}
	return p[:1] + strings.Repeat("*", len(p)-2) + p[len(p)-1:]
}

// ----------------------------------------------------
// Public APIs
// ----------------------------------------------------

// รูปคนขับ: host จาก ResolveCameraHosts + path คงที่ (channel 2)
func FetchDriverImage(cfg *config.Config, gateNo string) (string, error) {
	hosts := cfg.ResolveCameraHosts(gateNo)
	key, host := pickHost(hosts, []string{
		"driver", "dri", "drv", "face", "driver_out", "out_driver", "cam2",
	})
	if strings.TrimSpace(host) == "" {
		return "", fmt.Errorf("driver host not configured (gate=%s)", gateNo)
	}

	b64, _, err := tryFetchExact(host, driverSnapshotPath, cfg.CameraUser, cfg.CameraPass)
	if err != nil || b64 == "" {
		return "", fmt.Errorf("driver fetch failed: %w", err)
	}

	_ = os.MkdirAll(snapshotDir, 0o755)
	raw, _ := base64.StdEncoding.DecodeString(b64)
	_ = os.WriteFile(filepath.Join(snapshotDir, fmt.Sprintf("%s_%s.jpg", gateNo, key)), raw, 0o644)

	return b64, nil
}

// รูปป้ายทะเบียน: host จาก ResolveCameraHosts + path คงที่ (channel 1)
func FetchLicensePlateImage(cfg *config.Config, gateNo string) (string, error) {
	hosts := cfg.ResolveCameraHosts(gateNo)
	key, host := pickHost(hosts, []string{
		"license_plate", "lic", "lpr", "lp", "plate", "license_plate_out", "lpr_out",
	})
	if strings.TrimSpace(host) == "" {
		return "", fmt.Errorf("license plate host not configured (gate=%s)", gateNo)
	}

	b64, _, err := tryFetchExact(host, lprSnapshotPath, cfg.CameraUser, cfg.CameraPass)
	if err != nil || b64 == "" {
		return "", fmt.Errorf("license plate fetch failed: %w", err)
	}

	_ = os.MkdirAll(snapshotDir, 0o755)
	raw, _ := base64.StdEncoding.DecodeString(b64)
	_ = os.WriteFile(filepath.Join(snapshotDir, fmt.Sprintf("%s_%s.jpg", gateNo, key)), raw, 0o644)

	return b64, nil
}

// (optional) ตัวเดิม: ดึงพร้อมกันหลาย host (Digest only) ไว้ debug/retro-compat
func FetchImagesConcurrently(cfg *config.Config, gateNo string) map[string]string {
	hosts := cfg.ResolveCameraHosts(gateNo)

	log.Printf("[DEBUG] Camera hosts for gate %s: %+v (user=%s, pass=%s)",
		gateNo, hosts, cfg.CameraUser, maskPass(cfg.CameraPass))

	type res struct{ k, v string }
	ch := make(chan res, len(hosts))
	out := make(map[string]string, len(hosts))

	_ = os.MkdirAll(snapshotDir, 0o755)

	for key, host := range hosts {
		if strings.TrimSpace(host) == "" {
			log.Printf("[DEBUG] %s host empty (gate=%s)", key, gateNo)
			continue
		}
		go func(k, h string) {
			b64, _, err := tryFetchAll(h, cfg.CameraUser, cfg.CameraPass)
			if err != nil || b64 == "" {
				log.Printf("[DEBUG] fetch fail for %s (%s): %v", k, h, err)
				ch <- res{k: k, v: ""}
				return
			}
			//raw, _ := base64.StdEncoding.DecodeString(b64)
			//filename := filepath.Join(snapshotDir, fmt.Sprintf("%s_%s.jpg", gateNo, k))
			//if err := os.WriteFile(filename, raw, 0o644); err != nil {
			//	log.Printf("[DEBUG] write fail: %v", err)
			//} else {
			//	log.Printf("[DEBUG] saved snapshot %s -> %s", k, filename)
			//}
			//ch <- res{k: k, v: b64}
		}(key, host)
	}

	timeout := time.After(3 * time.Second)
	for i := 0; i < len(hosts); i++ {
		select {
		case r := <-ch:
			if r.v != "" {
				out[r.k] = r.v
			}
		case <-timeout:
			return out
		}
	}
	return out
}
