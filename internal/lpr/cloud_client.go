package lpr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"GO_LANG_WORKSPACE/internal/config"
)

type CustomerLookupClient interface {
	GetCustomerID(ctx context.Context, correctedPlate, parkingCode string) (map[string]any, error)
}

type CloudClient struct {
	serverURL  string
	httpClient *http.Client
}

func NewCloudClient(serverURL string) *CloudClient {
	return &CloudClient{
		serverURL: strings.TrimRight(serverURL, "/"),
		httpClient: &http.Client{
			Timeout:   6 * time.Second,
			Transport: config.NewHTTPTransport(),
		},
	}
}

func (c *CloudClient) GetCustomerID(ctx context.Context, correctedPlate, parkingCode string) (map[string]any, error) {
	if strings.TrimSpace(c.serverURL) == "" {
		return nil, fmt.Errorf("SERVER_URL not configured")
	}

	base, err := url.Parse(c.serverURL)
	if err != nil {
		return nil, fmt.Errorf("invalid SERVER_URL: %w", err)
	}
	base.Path = path.Join(base.Path, "/api/v2-202402/order/get-customer-id")

	q := base.Query()
	q.Set("license_plate", correctedPlate)
	q.Set("parking_code", parkingCode)
	base.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	out := map[string]any{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode get-customer-id response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, fmt.Errorf("get-customer-id status %d", resp.StatusCode)
	}
	return out, nil
}
