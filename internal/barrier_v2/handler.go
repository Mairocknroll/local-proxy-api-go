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

// OpenBarrier godoc
// @Summary      เปิดไม้กั้น (Barrier)
// @Description  สั่งเปิดไม้กั้นตามทิศทางและหมายเลขประตู\n
// @Description  - direction: ENT (ขาเข้า) หรือ EXT (ขาออก)\n
// @Description  - gate: หมายเลขประตู (ตัวเลขตามระบบ)
// @Tags         barrier
// @Produce      json
// @Param        direction  path      string  true  "ทิศทาง"  Enums(ENT,EXT)
// @Param        gate       path      string  true  "หมายเลขประตู"
// @Success      200        {object}  map[string]interface{}  "opened"
// @Failure      400        {object}  map[string]interface{}  "invalid direction/gate"
// @Failure      404        {object}  map[string]interface{}  "IP not found for this gate"
// @Failure      500        {object}  map[string]interface{}  "modbus error"
// @Router       /api/v2-202402/gate/open-barrier/{direction}/{gate} [get]
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

// CloseBarrier godoc
// @Summary      ปิดไม้กั้น (Barrier)
// @Description  สั่งปิดไม้กั้นตามทิศทางและหมายเลขประตู\n
// @Description  - direction: ENT (ขาเข้า) หรือ EXT (ขาออก)\n
// @Description  - gate: หมายเลขประตู (ตัวเลขตามระบบ)
// @Tags         barrier
// @Produce      json
// @Param        direction  path      string  true  "ทิศทาง"  Enums(ENT,EXT)
// @Param        gate       path      string  true  "หมายเลขประตู"
// @Success      200        {object}  map[string]interface{}  "closed"
// @Failure      400        {object}  map[string]interface{}  "invalid direction/gate"
// @Failure      404        {object}  map[string]interface{}  "IP not found for this gate"
// @Failure      500        {object}  map[string]interface{}  "modbus error"
// @Router       /api/v2-202402/gate/close-barrier/{direction}/{gate} [get]
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

// OpenBarrierZone godoc
// @Summary      เปิดไม้กั้นโซน (Barrier)
// @Description  สั่งเปิดไม้กั้นตามทิศทางและหมายเลขประตู\n
// @Description  - direction: ENT (ขาเข้า) หรือ EXT (ขาออก)\n
// @Description  - gate: หมายเลขประตู (ตัวเลขตามระบบ)
// @Tags         barrier
// @Produce      json
// @Param        direction  path      string  true  "ทิศทาง"  Enums(ENT,EXT)
// @Param        gate       path      string  true  "หมายเลขประตู"
// @Success      200        {object}  map[string]interface{}  "opened"
// @Failure      400        {object}  map[string]interface{}  "invalid direction/gate"
// @Failure      404        {object}  map[string]interface{}  "IP not found for this gate"
// @Failure      500        {object}  map[string]interface{}  "modbus error"
// @Router       /api/v2-202402/gate/open-zoning/{direction}/{gate} [get]
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
	if err := toggleCoil(ip, coil); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": true, "message": "opened", "data": nil})
}

// CloseBarrierZone godoc
// @Summary      ปิดไม้กั้นโซน (Barrier)
// @Description  สั่งเปิดไม้กั้นตามทิศทางและหมายเลขประตู\n
// @Description  - direction: ENT (ขาเข้า) หรือ EXT (ขาออก)\n
// @Description  - gate: หมายเลขประตู (ตัวเลขตามระบบ)
// @Tags         barrier
// @Produce      json
// @Param        direction  path      string  true  "ทิศทาง"  Enums(ENT,EXT)
// @Param        gate       path      string  true  "หมายเลขประตู"
// @Success      200        {object}  map[string]interface{}  "closed"
// @Failure      400        {object}  map[string]interface{}  "invalid direction/gate"
// @Failure      404        {object}  map[string]interface{}  "IP not found for this gate"
// @Failure      500        {object}  map[string]interface{}  "modbus error"
// @Router       /api/v2-202402/gate/close-zoning/{direction}/{gate} [get]
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

	coil := 1
	if err := toggleCoil(ip, coil); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": true, "message": "closed", "data": nil})
}

// ---------- Trigger Coil (long pulse สำหรับแสดงสี LED สถานะ) ----------

func getModbusTriggerPulse() time.Duration {
	return time.Duration(getenvInt("MODBUS_TRIGGER_PULSE_MS", 3000)) * time.Millisecond
}

// triggerCoil เหมือน toggleCoil แต่ใช้ pulse ที่ยาวกว่า (default 3 วินาที)
func triggerCoil(ip string, coilAddress int) error {
	addr := fmt.Sprintf("%s:%s", ip, getModbusPort())

	h := modbus.NewTCPClientHandler(addr)
	h.Timeout = getModbusTimeout()
	h.SlaveId = getModbusSlaveID()

	if err := h.Connect(); err != nil {
		return fmt.Errorf("modbus connect: %w", err)
	}
	defer h.Close()

	client := modbus.NewClient(h)

	// ON
	if _, err := client.WriteSingleCoil(uint16(coilAddress), 0xFF00); err != nil {
		return fmt.Errorf("coil ON: %w", err)
	}
	time.Sleep(getModbusTriggerPulse()) // pulse ยาว 3 วินาที
	// OFF
	if _, err := client.WriteSingleCoil(uint16(coilAddress), 0x0000); err != nil {
		return fmt.Errorf("coil OFF: %w", err)
	}
	return nil
}

// TriggerMainSuccessByGate แสดงไฟ เขียว (coil 0) = main สำเร็จ
// ทำงานแบบ non-blocking (goroutine) ภายใน
func TriggerMainSuccessByGate(direction, gate string) {
	go func() {
		if !reDirection.MatchString(direction) || !reGate.MatchString(gate) {
			return
		}
		ip := getDeviceIP(direction, gate, "GATE")
		if ip == "" {
			return
		}
		if err := triggerCoil(ip, 0); err != nil {
			fmt.Printf("[triggerMain] coil 0 (green) error: %v\n", err)
		}
	}()
}

// TriggerZoneSuccessByGate แสดงไฟ ส้ม-เหลือง (coil 3) = zone สำเร็จ
// ทำงานแบบ non-blocking (goroutine) ภายใน
func TriggerZoneSuccessByGate(direction, gate string) {
	go func() {
		if !reDirection.MatchString(direction) || !reGate.MatchString(gate) {
			return
		}
		ip := getDeviceIP(direction, gate, "ZONE")
		if ip == "" {
			return
		}
		if err := triggerCoil(ip, 3); err != nil {
			fmt.Printf("[triggerZone] coil 3 (orange-yellow) error: %v\n", err)
		}
	}()
}

// TriggerReserveSuccessByGate แสดงไฟ น้ำเงิน (coil 2) = reserve สำเร็จ
// ทำงานแบบ non-blocking (goroutine) ภายใน
func TriggerReserveSuccessByGate(direction, gate string) {
	go func() {
		if !reDirection.MatchString(direction) || !reGate.MatchString(gate) {
			return
		}
		ip := getDeviceIP(direction, gate, "RESE")
		if ip == "" {
			return
		}
		if err := triggerCoil(ip, 2); err != nil {
			fmt.Printf("[triggerReserve] coil 2 (blue) error: %v\n", err)
		}
	}()
}

// OpenBarrierByGate เปิดไม้กั้นตาม direction และ gate (สำหรับเรียกจาก package อื่น)
func OpenBarrierByGate(direction, gate string) error {
	if !reDirection.MatchString(direction) {
		return fmt.Errorf("invalid direction: %s (must be ENT or EXT)", direction)
	}
	if !reGate.MatchString(gate) {
		return fmt.Errorf("invalid gate: %s (must be numeric)", gate)
	}

	ip := getDeviceIP(direction, gate, "GATE")
	if ip == "" {
		return fmt.Errorf("IP not found for gate %s %s", direction, gate)
	}

	if err := toggleCoil(ip, 1); err != nil {
		return fmt.Errorf("failed to open barrier: %w", err)
	}

	return nil
}

// OpenZoningByGate เปิดไม้กั้น zone ตาม direction และ gate (สำหรับเรียกจาก package อื่น)
func OpenZoningByGate(direction, gate string) error {
	if !reDirection.MatchString(direction) {
		return fmt.Errorf("invalid direction: %s (must be ENT or EXT)", direction)
	}
	if !reGate.MatchString(gate) {
		return fmt.Errorf("invalid gate: %s (must be numeric)", gate)
	}

	ip := getDeviceIP(direction, gate, "ZONE")
	if ip == "" {
		return fmt.Errorf("IP not found for zone %s %s", direction, gate)
	}

	if err := toggleCoil(ip, 1); err != nil {
		return fmt.Errorf("failed to open zone barrier: %w", err)
	}

	return nil
}

// OpenReserveBarrierByGate เปิดไม้กั้น reserve ตาม direction และ gate
// ใช้ key เช่น ENT_RESE_01, EXT_RESE_01
func OpenReserveBarrierByGate(direction, gate string) error {
	if !reDirection.MatchString(direction) {
		return fmt.Errorf("invalid direction: %s (must be ENT or EXT)", direction)
	}
	if !reGate.MatchString(gate) {
		return fmt.Errorf("invalid gate: %s (must be numeric)", gate)
	}

	// ใช้ location = "RESE"
	ip := getDeviceIP(direction, gate, "RESE")
	if ip == "" {
		return fmt.Errorf("IP not found for reserve barrier %s %s", direction, gate)
	}

	if err := toggleCoil(ip, 1); err != nil {
		return fmt.Errorf("failed to open reserve barrier: %w", err)
	}

	return nil
}
