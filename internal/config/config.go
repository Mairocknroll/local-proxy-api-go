package config

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Config เก็บค่า environment หลัก ๆ ของ edge service
type Config struct {
	ServerURL    string
	ParkingCode  string
	Addr         string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration

	CameraUser string
	CameraPass string

	ImageAPIKey             string
	LPRMonitorEnabled       bool
	LPRRescueToKioskEnabled bool
	LPRRescueWindow         time.Duration
	CORSAllowedOrigins      []string
}

func Load() *Config {
	return &Config{
		ServerURL:    getenv("SERVER_URL", "https://api-pms.jparkdev.co"),
		ParkingCode:  getenv("PARKING_CODE", "ro24050002"),
		Addr:         getenv("ADDR", "0.0.0.0:8000"),
		ReadTimeout:  durEnv("READ_TIMEOUT", 10*time.Second),
		WriteTimeout: durEnv("WRITE_TIMEOUT", 120*time.Second),
		IdleTimeout:  durEnv("IDLE_TIMEOUT", 120*time.Second),

		CameraUser: getenv("CAMERA_USER", "admin"),
		CameraPass: getenv("CAMERA_PASS", "Jp@rk1ng"),

		ImageAPIKey:             getenv("IMAGE_API_KEY", ""),
		LPRMonitorEnabled:       boolEnv("LPR_MONITOR_ENABLED", true),
		LPRRescueToKioskEnabled: boolEnv("LPR_RESCUE_TO_KIOSK_ENABLED", true),
		LPRRescueWindow:         time.Duration(intEnv("LPR_RESCUE_WINDOW_SECONDS", 30)) * time.Second,
		CORSAllowedOrigins:      csvEnv("CORS_ALLOWED_ORIGINS", "http://localhost:5173,http://127.0.0.1:5173,http://172.20.9.2:5173"),
	}
}

// คืน host ของกล้องตาม gate
func (c *Config) ResolveCameraHosts(gateNo string) map[string]string {
	// ✅ padding gateNo ให้เป็นเลข 2 หลัก เช่น 1 -> "01"
	padded := fmt.Sprintf("%02s", gateNo)

	return map[string]string{
		"lpr_out":           os.Getenv("LPR_OUT_" + padded),
		"license_plate_out": os.Getenv("LIC_OUT_" + padded),
		"driver_out":        os.Getenv("DRI_OUT_" + padded),
	}
}

func (c *Config) ResolveCameraLicExitHosts(gateNo string) map[string]string {
	// ✅ padding gateNo ให้เป็นเลข 2 หลัก เช่น 1 -> "01"
	padded := fmt.Sprintf("%02s", gateNo)

	return map[string]string{
		"license_plate_out": os.Getenv("LIC_OUT_" + padded),
	}
}

func (c *Config) ResolveCameraLprExitHosts(gateNo string) map[string]string {
	// ✅ padding gateNo ให้เป็นเลข 2 หลัก เช่น 1 -> "01"
	padded := fmt.Sprintf("%02s", gateNo)

	return map[string]string{
		"lpr_out": os.Getenv("LPR_OUT_" + padded),
	}
}

func (c *Config) ResolveCameraEntranceHosts(gateNo string) map[string]string {
	// ✅ padding gateNo ให้เป็นเลข 2 หลัก เช่น 1 -> "01"
	padded := fmt.Sprintf("%02s", gateNo)

	return map[string]string{
		"driver_in": os.Getenv("DRI_IN_" + padded),
	}
}

func (c *Config) ResolveCameraEntranceLicensePLateHosts(gateNo string) map[string]string {
	// ✅ padding gateNo ให้เป็นเลข 2 หลัก เช่น 1 -> "01"
	padded := fmt.Sprintf("%02s", gateNo)

	return map[string]string{
		"lic_in": os.Getenv("LIC_IN_" + padded),
	}
}

// NewHTTPServer คืน http.Server ที่จูน Transport/Timeout มาค่อนข้างเหมาะกับ I/O เยอะ ๆ
func NewHTTPServer(cfg *Config, h http.Handler) *http.Server {
	return &http.Server{
		Addr:         cfg.Addr,
		Handler:      h,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}
}

// ---------- Middlewares ----------

func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		reqID := c.GetHeader("X-Request-Id")
		if reqID == "" {
			reqID = uuid.NewString()
		}
		c.Set("request_id", reqID)
		c.Writer.Header().Set("X-Request-Id", reqID)
		c.Next()
	}
}

func LoggerMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		d := time.Since(start)
		reqID, _ := c.Get("request_id")
		log.Printf("%s %s %d %s rid=%v", c.Request.Method, c.Request.URL.Path, c.Writer.Status(), d, reqID)
	}
}

func CORSMiddleware(cfg *Config) gin.HandlerFunc {
	allowedOrigins := map[string]struct{}{}
	if cfg != nil {
		for _, origin := range cfg.CORSAllowedOrigins {
			if origin = strings.TrimSpace(origin); origin != "" {
				allowedOrigins[origin] = struct{}{}
			}
		}
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if _, ok := allowedOrigins[origin]; ok {
			headers := c.Writer.Header()
			headers.Set("Access-Control-Allow-Origin", origin)
			headers.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			headers.Set("Access-Control-Allow-Headers", "Origin, Content-Type, Accept, Authorization, X-Request-Id")
			headers.Set("Vary", "Origin")
		}

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// ApiKeyMiddleware ตรวจสอบ header X-Api-Key ให้ตรงกับ cfg.ImageAPIKey
func ApiKeyMiddleware(cfg *Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if cfg.ImageAPIKey == "" {
			// ถ้าไม่ได้ตั้งค่า key ให้ผ่านไปก่อน (backward-compat)
			c.Next()
			return
		}
		if c.GetHeader("X-Api-Key") != cfg.ImageAPIKey {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"status":  false,
				"message": "unauthorized: invalid or missing X-Api-Key",
			})
			return
		}
		c.Next()
	}
}

// ---------- Transport ----------

func NewHTTPTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 60 * time.Second,
		}).DialContext,
		MaxIdleConns:        512,
		MaxIdleConnsPerHost: 256,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 5 * time.Second,
		ForceAttemptHTTP2:   true,
	}
}

// ---------- helpers ----------

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func durEnv(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func boolEnv(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		switch strings.ToLower(v) {
		case "1", "true", "yes", "y", "on":
			return true
		case "0", "false", "no", "n", "off":
			return false
		}
	}
	return def
}

func intEnv(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func csvEnv(key string, def string) []string {
	raw := getenv(key, def)
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			out = append(out, value)
		}
	}
	return out
}
