package utils

import (
	"GO_LANG_WORKSPACE/internal/config"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	digest "github.com/icholy/digest"
)

const (
	perShotTimeout = 1500 * time.Millisecond
	minUsefulBytes = 800
	snapshotDir    = "./snapshots"
)

var candidatePaths = []string{
	"/ISAPI/Streaming/channels/1/picture",
	"/ISAPI/Streaming/channels/101/picture",
	"/ISAPI/Streaming/channels/102/picture",
	"/ISAPI/Streaming/channels/201/picture",
	"/ISAPI/Streaming/channels/202/picture",
	"/Streaming/channels/1/picture",
	"/Streaming/channels/101/picture",
	"/ISAPI/ContentMgmt/picture",
}

func FetchImagesConcurrently(cfg *config.Config, gateNo string) map[string]string {
	hosts := cfg.ResolveCameraHosts(gateNo)

	log.Printf("[DEBUG] Camera hosts for gate %s: %+v (user=%s, pass=%s)",
		gateNo, hosts, cfg.CameraUser, cfg.CameraPass)

	type res struct{ k, v string }
	ch := make(chan res, len(hosts))
	out := make(map[string]string, len(hosts))

	_ = os.MkdirAll(snapshotDir, 0o755)

	for key, host := range hosts {
		if host == "" {
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
			raw, _ := base64.StdEncoding.DecodeString(b64)
			filename := filepath.Join(snapshotDir, fmt.Sprintf("%s_%s.jpg", gateNo, k))
			if err := os.WriteFile(filename, raw, 0o644); err != nil {
				log.Printf("[DEBUG] write fail: %v", err)
			} else {
				log.Printf("[DEBUG] saved snapshot %s -> %s", k, filename)
			}
			ch <- res{k: k, v: b64}
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

func tryFetchAll(host, user, pass string) (string, int, error) {
	var lastErr error
	var lastStatus int

	for _, p := range candidatePaths {
		url := "http://" + host + p

		if b, st, err := fetchDigest(url, user, pass); err == nil && st == 200 && len(b) > minUsefulBytes {
			return base64.StdEncoding.EncodeToString(b), st, nil
		} else {
			lastErr, lastStatus = err, st
			if st == 404 {
				continue
			}
		}

		if b, st, err := fetchBasic(url, user, pass); err == nil && st == 200 && len(b) > minUsefulBytes {
			return base64.StdEncoding.EncodeToString(b), st, nil
		} else {
			lastErr, lastStatus = err, st
		}
	}
	return "", lastStatus, lastErr
}

func fetchDigest(url, user, pass string) ([]byte, int, error) {
	tp := &digest.Transport{Username: user, Password: pass}
	client := &http.Client{Transport: tp, Timeout: perShotTimeout}

	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Accept", "image/jpeg")

	ctx, cancel := context.WithTimeout(context.Background(), perShotTimeout)
	defer cancel()
	req = req.WithContext(ctx)

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		io.Copy(io.Discard, resp.Body)
		return nil, resp.StatusCode, fmt.Errorf("status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	return b, resp.StatusCode, err
}

func fetchBasic(url, user, pass string) ([]byte, int, error) {
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.SetBasicAuth(user, pass)
	req.Header.Set("Accept", "image/jpeg")

	ctx, cancel := context.WithTimeout(context.Background(), perShotTimeout)
	defer cancel()
	req = req.WithContext(ctx)

	client := &http.Client{Timeout: perShotTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		io.Copy(io.Discard, resp.Body)
		return nil, resp.StatusCode, fmt.Errorf("status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	return b, resp.StatusCode, err
}
