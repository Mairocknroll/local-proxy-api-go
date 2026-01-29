// Package proxy provides a wildcard reverse proxy handler
// that forwards requests to customer API endpoints.
package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
)

// Handler holds the HTTP client for proxy requests
type Handler struct {
	httpClient *http.Client
	// targetBaseURL is the base URL of the customer API
	// e.g., "http://172.16.93.13:3003"
	targetBaseURL string
	// barrierToken is the Bearer token for authorization
	barrierToken string
}

// NewHandler creates a new proxy handler with the given target base URL and token
func NewHandler(targetBaseURL, barrierToken string) *Handler {
	return &Handler{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		targetBaseURL: targetBaseURL,
		barrierToken:  barrierToken,
	}
}

// ProxyWildcard handles wildcard proxy requests
// It forwards the request path and body to the target API
//
// @Summary      Proxy request to customer API
// @Description  Forward any request to the customer API endpoint
// @Tags         proxy
// @Accept       json
// @Produce      json
// @Param        path path string true "API path to forward"
// @Success      200 {object} map[string]interface{}
// @Failure      400 {object} map[string]string
// @Failure      502 {object} map[string]string
// @Router       /api/v1/proxy/{path} [post]
func (h *Handler) ProxyWildcard(c *gin.Context) {
	// Get the wildcard path (everything after /proxy/)
	proxyPath := c.Param("path")
	if proxyPath == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  false,
			"message": "proxy path is required",
		})
		return
	}

	// Build the target URL
	targetURL, err := url.Parse(h.targetBaseURL)
	if err != nil {
		log.Printf("[proxy] invalid target base URL: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":  false,
			"message": "invalid proxy configuration",
		})
		return
	}
	targetURL.Path = proxyPath

	// Copy query parameters
	targetURL.RawQuery = c.Request.URL.RawQuery

	// Read the request body
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		log.Printf("[proxy] failed to read request body: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  false,
			"message": "failed to read request body",
		})
		return
	}

	// Log the incoming request
	log.Printf("[proxy] forwarding %s %s -> %s", c.Request.Method, proxyPath, targetURL.String())
	if len(bodyBytes) > 0 {
		log.Printf("[proxy] request body: %s", string(bodyBytes))
	}

	// Create the proxy request
	proxyReq, err := http.NewRequestWithContext(
		c.Request.Context(),
		c.Request.Method,
		targetURL.String(),
		bytes.NewReader(bodyBytes),
	)
	if err != nil {
		log.Printf("[proxy] failed to create request: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":  false,
			"message": "failed to create proxy request",
		})
		return
	}

	// Copy headers from the original request
	for key, values := range c.Request.Header {
		for _, value := range values {
			proxyReq.Header.Add(key, value)
		}
	}

	// Ensure Content-Type is set for JSON
	if proxyReq.Header.Get("Content-Type") == "" {
		proxyReq.Header.Set("Content-Type", "application/json")
	}

	// Add Bearer token if configured
	if h.barrierToken != "" {
		proxyReq.Header.Set("Authorization", "Bearer "+h.barrierToken)
	}

	// Send the request to the target
	resp, err := h.httpClient.Do(proxyReq)
	if err != nil {
		log.Printf("[proxy] request to target failed: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{
			"status":  false,
			"message": "failed to reach target API",
			"error":   err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	// Read the response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[proxy] failed to read response body: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{
			"status":  false,
			"message": "failed to read target response",
		})
		return
	}

	// Log the response
	log.Printf("[proxy] response status: %d, body: %s", resp.StatusCode, string(respBody))

	// Copy response headers
	for key, values := range resp.Header {
		for _, value := range values {
			c.Header(key, value)
		}
	}

	// Try to parse as JSON and return
	var jsonResp map[string]interface{}
	if err := json.Unmarshal(respBody, &jsonResp); err == nil {
		c.JSON(resp.StatusCode, jsonResp)
		return
	}

	// If not JSON, return as plain text
	c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), respBody)
}

// ProxyAny handles any HTTP method for wildcard proxy
func (h *Handler) ProxyAny(c *gin.Context) {
	h.ProxyWildcard(c)
}
