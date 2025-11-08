package config

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Config เก็บค่า environment หลัก ๆ ของ edge service
type Config struct {
	ServerURL    string
	ParkingCode  string
	Addr         string
	SelfHost     string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration

	CameraUser string
	CameraPass string
}

func Load() *Config {
	return &Config{
		ServerURL:    getenv("SERVER_URL", "https://api-pms.jparkdev.co"),
		ParkingCode:  getenv("PARKING_CODE", "ro24050002"),
		Addr:         getenv("ADDR", "0.0.0.0:8000"),
		SelfHost:     getenv("SELF_HOST", "http://localhost:8000"),
		ReadTimeout:  durEnv("READ_TIMEOUT", 10*time.Second),
		WriteTimeout: durEnv("WRITE_TIMEOUT", 120*time.Second),
		IdleTimeout:  durEnv("IDLE_TIMEOUT", 120*time.Second),

		CameraUser: getenv("CAMERA_USER", "admin"),
		CameraPass: getenv("CAMERA_PASS", "12345"),
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

func (c *Config) ResolveCameraEntranceHosts(gateNo string) map[string]string {
	// ✅ padding gateNo ให้เป็นเลข 2 หลัก เช่น 1 -> "01"
	padded := fmt.Sprintf("%02s", gateNo)

	return map[string]string{
		"driver_in": os.Getenv("DRI_IN_" + padded),
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
