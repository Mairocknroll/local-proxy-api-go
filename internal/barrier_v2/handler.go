package barrier_v2

import (
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/goburrow/modbus"
)

// ---------- ENV helpers ----------
func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
func getenvInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// ---------- Dynamic getters (อ่าน ENV ตอนเรียกใช้งาน) ----------
func getModbusPort() string {
	return getenv("MODBUS_PORT", "504")
}
func getModbusTimeout() time.Duration {
	return time.Duration(getenvInt("MODBUS_TIMEOUT_MS", 5000)) * time.Millisecond
}
func getModbusPulse() time.Duration {
	return time.Duration(getenvInt("MODBUS_PULSE_MS", 1000)) * time.Millisecond
}
func getModbusSlaveID() byte {
	return byte(getenvInt("MODBUS_SLAVE_ID", 1))
}

// ---------- Validators ----------
var (
	reDirection = regexp.MustCompile(`^(ENT|EXT)$`)
	reGate      = regexp.MustCompile(`^[0-9]+$`)
)

// ---------- Utilities ----------
func pad2(g string) string {
	if n, err := strconv.Atoi(g); err == nil {
		return fmt.Sprintf("%02d", n)
	}
	if len(g) == 1 {
		return "0" + g
	}
	return g
}
func getDeviceIP(direction, gate, location string) string {
	key := fmt.Sprintf("%s_%s_%s", direction, location, pad2(gate)) // เช่น ENT_ZONE_01
	return os.Getenv(key)
}

// ---------- Modbus ----------
func toggleCoil(ip string, coilAddress int) error {
	addr := fmt.Sprintf("%s:%s", ip, getModbusPort())

	h := modbus.NewTCPClientHandler(addr)
	h.Timeout = getModbusTimeout()
	h.SlaveId = getModbusSlaveID()
	// h.Logger = log.New(os.Stdout, "modbus ", log.LstdFlags)

	if err := h.Connect(); err != nil {
		return fmt.Errorf("modbus connect: %w", err)
	}
	defer h.Close()

	client := modbus.NewClient(h)

	// ON
	if _, err := client.WriteSingleCoil(uint16(coilAddress), 0xFF00); err != nil {
		return fmt.Errorf("coil ON: %w", err)
	}
	time.Sleep(getModbusPulse())
	// OFF
	if _, err := client.WriteSingleCoil(uint16(coilAddress), 0x0000); err != nil {
		return fmt.Errorf("coil OFF: %w", err)
	}
	return nil
}

// OpenBarrier /open-barrier/:direction/:gate
func OpenBarrier(c *gin.Context) {
	direction := c.Param("direction")
	gate := c.Param("gate")

	if !reDirection.MatchString(direction) {
		c.JSON(http.StatusBadRequest, gin.H{"status": false, "message": "invalid direction (ENT|EXT)"})
		return
	}
	if !reGate.MatchString(gate) {
		c.JSON(http.StatusBadRequest, gin.H{"status": false, "message": "invalid gate number"})
		return
	}
	ip := getDeviceIP(direction, gate, "GATE")
	if ip == "" {
		c.JSON(http.StatusNotFound, gin.H{"status": false, "message": "IP not found for this gate"})
		return
	}
	if err := toggleCoil(ip, 1); err != nil { // coil 1 = OPEN (ปรับตามหน้างาน)
		c.JSON(http.StatusInternalServerError, gin.H{"status": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": true, "message": "opened", "data": nil})
}

// CloseBarrier /close-barrier/:direction/:gate
func CloseBarrier(c *gin.Context) {
	direction := c.Param("direction")
	gate := c.Param("gate")

	if !reDirection.MatchString(direction) {
		c.JSON(http.StatusBadRequest, gin.H{"status": false, "message": "invalid direction (ENT|EXT)"})
		return
	}
	if !reGate.MatchString(gate) {
		c.JSON(http.StatusBadRequest, gin.H{"status": false, "message": "invalid gate number"})
		return
	}
	ip := getDeviceIP(direction, gate, "GATE")
	if ip == "" {
		c.JSON(http.StatusNotFound, gin.H{"status": false, "message": "IP not found for this gate"})
		return
	}
	if err := toggleCoil(ip, 4); err != nil { // coil 4 = CLOSE (ปรับตามหน้างาน)
		c.JSON(http.StatusInternalServerError, gin.H{"status": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": true, "message": "closed", "data": nil})
}

// OpenZoning GET /open-zoning/:direction/:gate
func OpenZoning(c *gin.Context) {
	direction := c.Param("direction")
	gate := c.Param("gate")

	if !reDirection.MatchString(direction) {
		c.JSON(http.StatusBadRequest, gin.H{"status": false, "message": "invalid direction (ENT|EXT)"})
		return
	}
	if !reGate.MatchString(gate) {
		c.JSON(http.StatusBadRequest, gin.H{"status": false, "message": "invalid gate number"})
		return
	}

	ip := getDeviceIP(direction, gate, "ZONE")
	if ip == "" {
		c.JSON(http.StatusNotFound, gin.H{"status": false, "message": "IP not found for this gate"})
		return
	}

	coil := 1
	if direction == "EXT" {
		coil = 3
	}
	if err := toggleCoil(ip, coil); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": true, "message": "opened", "data": nil})
}

// CloseZoning GET /open-zoning/:direction/:gate
func CloseZoning(c *gin.Context) {
	direction := c.Param("direction")
	gate := c.Param("gate")

	if !reDirection.MatchString(direction) {
		c.JSON(http.StatusBadRequest, gin.H{"status": false, "message": "invalid direction (ENT|EXT)"})
		return
	}
	if !reGate.MatchString(gate) {
		c.JSON(http.StatusBadRequest, gin.H{"status": false, "message": "invalid gate number"})
		return
	}

	ip := getDeviceIP(direction, gate, "ZONE")
	if ip == "" {
		c.JSON(http.StatusNotFound, gin.H{"status": false, "message": "IP not found for this gate"})
		return
	}

	coil := 2
	if direction == "EXT" {
		coil = 4
	}
	if err := toggleCoil(ip, coil); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": true, "message": "closed", "data": nil})
}
