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

	// PD (Parking Data) proxy configuration
	UsePD          bool   // USE_PD=true จะเปิดใช้งานการส่งข้อมูลไปยัง PD API
	PDLocalURL     string // PD_LOCAL_URL เช่น http://172.16.93.13:3003
	PDBarrierToken string // PD_BARRIER_TOKEN สำหรับ authentication
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

		// PD proxy config
		UsePD:          boolEnv("USE_PD", false),
		PDLocalURL:     getenv("PD_LOCAL_URL", "http://172.16.93.13:3003"),
		PDBarrierToken: getenv("PD_BARRIER_TOKEN", ""),
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
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	// รองรับ: true, TRUE, 1, yes, YES
	switch strings.ToLower(v) {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	default:
		return def
	}
}
