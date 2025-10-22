// ------------------------------------------------------------
// XML models (ลองมี namespace ก่อน แล้วค่อย fallback no-NS)
// โครงนี้ match กับ VerifyMember/VerifyLicensePlateOut เดิม
// ------------------------------------------------------------

package zoning

import (
	"GO_LANG_WORKSPACE/internal/config"
	"GO_LANG_WORKSPACE/internal/utils"
	"GO_LANG_WORKSPACE/internal/ws"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	cfg        *config.Config
	hub        *ws.Hub
	httpClient *http.Client
	deduper    *utils.Deduper
}

func NewHandler(cfg *config.Config, hub *ws.Hub) *Handler {
	return &Handler{
		cfg: cfg,
		hub: hub,
		httpClient: &http.Client{
			Timeout:   6 * time.Second,
			Transport: config.NewHTTPTransport(),
		},
		deduper: utils.NewDeduper(30 * time.Second),
	}
}

type eventXML struct {
	XMLName   xml.Name `xml:"EventNotificationAlert"`
	UUID      string   `xml:"UUID"`
	DateTime  string   `xml:"dateTime"`
	IPAddress string   `xml:"ipAddress"`

	ANPR struct {
		LicensePlate string `xml:"licensePlate"`
		VehicleType  string `xml:"vehicleType"`
	} `xml:"ANPR"`
}

type eventXMLNoNS struct {
	UUID      string `xml:"UUID"`
	DateTime  string `xml:"dateTime"`
	IPAddress string `xml:"ipAddress"`

	ANPR struct {
		LicensePlate string `xml:"licensePlate"`
		VehicleType  string `xml:"vehicleType"`
	} `xml:"ANPR"`
}

// ------------------------------------------------------------
// Helpers (ยึดสไตล์ไฟล์ order เดิม)
// ------------------------------------------------------------

func (h *Handler) parseMultipartExpectXML(c *gin.Context) (xmlBuf, lpImg, dtImg []byte, ok bool) {
	ct := c.GetHeader("Content-Type")
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		c.String(http.StatusBadRequest, "Invalid request")
		return nil, nil, nil, false
	}
	mr := multipart.NewReader(c.Request.Body, params["boundary"])

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Println("[Parse Err]", err)
			continue
		}
		fn := part.FileName()
		if fn == "" {
			io.Copy(io.Discard, part)
			_ = part.Close()
			continue
		}
		buf, _ := io.ReadAll(part)
		_ = part.Close()

		switch {
		case strings.HasSuffix(strings.ToLower(fn), ".xml"):
			xmlBuf = buf
		case fn == "licensePlatePicture.jpg":
			lpImg = buf
		case fn == "detectedImage.jpg" || fn == "pedestrianDetectionPicture.jpg":
			dtImg = buf
		}
	}
	if len(xmlBuf) == 0 {
		c.String(http.StatusBadRequest, "Missing XML file")
		return nil, nil, nil, false
	}
	return xmlBuf, lpImg, dtImg, true
}

func (h *Handler) parsePlateBundle(xmlBuf []byte) (plate, uuid, timeIn, ip, vehicleType string, ok bool) {
	var ev eventXML
	if err := xml.Unmarshal(xmlBuf, &ev); err == nil && strings.TrimSpace(ev.ANPR.LicensePlate) != "" {
		return strings.TrimSpace(ev.ANPR.LicensePlate),
			strings.TrimSpace(ev.UUID),
			strings.TrimSpace(ev.DateTime),
			strings.TrimSpace(ev.IPAddress),
			strings.TrimSpace(ev.ANPR.VehicleType),
			true
	}

	var ev2 eventXMLNoNS
	if err2 := xml.Unmarshal(xmlBuf, &ev2); err2 == nil && strings.TrimSpace(ev2.ANPR.LicensePlate) != "" {
		// NOTE: ยึดสไตล์เดิมของ VerifyMember (รักษา vehicleType ตาม struct แรกถ้าจะคง “ตรรกะเดิมเป๊ะๆ”)
		return strings.TrimSpace(ev2.ANPR.LicensePlate),
			strings.TrimSpace(ev2.UUID),
			strings.TrimSpace(ev2.DateTime),
			strings.TrimSpace(ev2.IPAddress),
			strings.TrimSpace(ev2.ANPR.VehicleType),
			true
	}
	return "", "", "", "", "", false
}

func (h *Handler) boolish(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(x, "true")
	case float64:
		return x != 0
	default:
		return false
	}
}

func (h *Handler) getUUIDFromData(m map[string]any) string {
	d, ok := m["data"].(map[string]any)
	if !ok {
		return ""
	}
	if u, ok := d["uuid"].(string); ok {
		return u
	}
	return ""
}

// ------------------------------------------------------------
// ZoningEntrance: POST /api/v2-202402/zoning/entrance/:zoning_code?gate_no=1
// ------------------------------------------------------------

func (h *Handler) ZoningEntrance(c *gin.Context) {
	t0 := time.Now()

	// Step 1: multipart
	xmlBuf, lpImg, dtImg, ok := h.parseMultipartExpectXML(c)
	if !ok {
		return
	}
	t1 := time.Since(t0)

	// Step 2: parse XML
	plate, _, _, ip, vehicleType, ok := h.parsePlateBundle(xmlBuf)
	if !ok {
		c.String(http.StatusBadRequest, "Failed to parse XML")
		return
	}
	t2 := time.Since(t0) - t1

	// Step 3: Background save PLP
	go h.postParkingLicensePlate(plate, ip, h.cfg.ParkingCode)
	t3 := time.Since(t0) - t1 - t2

	// Step 4: หาก unknown → broadcast แบบ minimal แล้วจบ
	gateNo := c.Query("gate_no")
	zoningCode := c.Param("zoning_code")
	// เดิม: room := "zoning_ent_" + zoningCode + "_" + gateNo
	room := fmt.Sprintf("entrance:%s:%s", zoningCode, gateNo)

	if plate == "unknown" {
		payload := map[string]any{
			"status":  false,
			"message": "cannot read license plate",
			"data": map[string]any{
				"license_plate":            plate,
				"license_plate_img_base64": base64.StdEncoding.EncodeToString(lpImg),
			},
		}
		h.broadcastJSON(room, payload)
		h.logZoningTiming("ENT", zoningCode, gateNo, plate, t1, t2, t3, 0, 0, 0, 0, t0)
		c.String(http.StatusOK, "File(s) uploaded successfully")
		return
	}

	// Step 5: Call transition (zoning_code = zoningCode)
	base, _ := url.Parse(h.cfg.ServerURL)
	base.Path = path.Join(base.Path, "/api/v1-202402/zoning/transition")

	reqBody := map[string]any{
		"license_plate":   plate,
		"parking_code":    h.cfg.ParkingCode,
		"zoning_code":     zoningCode,
		"vehicle_type_id": utils.VehicleType(vehicleType), // เหมือน VerifyMember (map → int)
		"gate_id":         gateNo,
	}
	transitionURL := base.String()

	resData, err := h.postJSON(transitionURL, reqBody)
	if err != nil {
		log.Printf("[transition][ENT] error: %v", err)
		c.String(http.StatusBadGateway, "transition failed")
		return
	}
	t4 := time.Since(t0) - t1 - t2 - t3

	// เติม base64 รูปป้ายเข้า data
	if resData != nil {
		if _, ok := resData["data"]; !ok {
			resData["data"] = map[string]any{}
		}
		if dm, ok := resData["data"].(map[string]any); ok {
			dm["license_plate_img_base64"] = base64.StdEncoding.EncodeToString(lpImg)
		}
	}

	// ถ้าสำเร็จ → BG collect-image
	if resData != nil && h.boolish(resData["status"]) {
		u := h.getUUIDFromData(resData)

		collectBase, _ := url.Parse(h.cfg.ServerURL)
		collectBase.Path = path.Join(collectBase.Path, "/api/v1-202402/zoning/collect-image/", u)
		collectURL := collectBase.String()

		payload := map[string]any{
			"license_plate":            plate,
			"park_code":                h.cfg.ParkingCode,
			"zoning_code":              zoningCode,
			"time_stamp":               time.Now().Format(time.RFC3339),
			"gate":                     "ent",
			"license_plate_img_base64": base64.StdEncoding.EncodeToString(lpImg),
			"driver_img_base_64":       base64.StdEncoding.EncodeToString(dtImg),
		}
		go func() {
			// PUT collect image
			_, err := h.putJSON(collectURL, payload)
			if err != nil {
				log.Printf("[collect-image][ENT] err: %v", err)
			}
		}()
	}
	t5 := time.Since(t0) - t1 - t2 - t3 - t4

	// Step 6: Broadcast
	h.broadcastJSON(room, resData)
	t6 := time.Since(t0) - t1 - t2 - t3 - t4 - t5

	gateStr := c.Query("gate_no") // "01", "1", ...
	gateNoE, err := strconv.Atoi(gateStr)
	if err != nil {
		// ถ้าอยาก safety หน่อย:
		log.Printf("invalid gate_no %q: %v", gateStr, err)
		c.String(http.StatusBadRequest, "invalid gate_no")
		return
	}

	// ENTRANCE
	envKey := fmt.Sprintf("HIK_LED_ZONE_ENT_%02d", gateNoE)
	// EXIT
	// envKey := fmt.Sprintf("HIK_LED_ZONE_EXT_%02d", gateNo)

	value, ok := os.LookupEnv(envKey) // ช่วยแยก "ไม่ได้ตั้ง" กับ "ตั้งแต่ค่าว่าง"
	if !ok || value == "" {
		log.Printf("Environment variable %s not found or empty", envKey)
		// จะ return เลยหรือข้ามการแสดง LED ไปก็ได้ แล้วทำงานต่อส่วนอื่น
		// return
	}

	// (Optional) แสดง LED
	disErr := utils.DisplayHexData(
		value,                    // screen_ip
		9999,                     // screen_port
		plate,                    // license_plate
		"ent",                    // direction
		"zone",                   // state_type
		fmt.Sprintf("%d THB", 0), // line3
	)
	if disErr != nil {
		fmt.Println("Error:", disErr)
	} else {
		fmt.Println("Packet sent successfully.")
	}

	// Log timing
	h.logZoningTiming("ENT", zoningCode, gateNo, plate, t1, t2, t3, t4, t5, t6, 0, t0)

	c.String(http.StatusOK, "File(s) uploaded successfully")
}

// ------------------------------------------------------------
// ZoningExit: POST /api/v2-202402/zoning/exit/:zoning_code?gate_no=1&next_zone=zn25050001
// ------------------------------------------------------------

func (h *Handler) ZoningExit(c *gin.Context) {
	t0 := time.Now()

	// Step 1: multipart
	xmlBuf, lpImg, dtImg, ok := h.parseMultipartExpectXML(c)
	if !ok {
		return
	}
	t1 := time.Since(t0)

	// Step 2: parse XML
	plate, _, _, ip, vehicleType, ok := h.parsePlateBundle(xmlBuf)
	if !ok {
		c.String(http.StatusBadRequest, "Failed to parse XML")
		return
	}
	t2 := time.Since(t0) - t1

	// Step 3: BG save
	go h.postParkingLicensePlate(plate, ip, h.cfg.ParkingCode)
	t3 := time.Since(t0) - t1 - t2

	gateNo := c.Query("gate_no")
	zoningCode := c.Param("zoning_code")
	nextZone := c.Query("next_zone")
	// เดิม: room := "zoning_ext_" + zoningCode + "_" + gateNo
	room := fmt.Sprintf("exit:%s:%s", zoningCode, gateNo)

	// unknown
	if plate == "unknown" {
		payload := map[string]any{
			"status":  false,
			"message": "cannot read license plate",
			"data": map[string]any{
				"license_plate":            plate,
				"license_plate_img_base64": base64.StdEncoding.EncodeToString(lpImg),
			},
		}
		h.broadcastJSON(room, payload)
		h.logZoningTiming("EXT", zoningCode, gateNo, plate, t1, t2, t3, 0, 0, 0, 0, t0)
		c.String(http.StatusOK, "File(s) uploaded successfully")
		return
	}

	// Step 4: transition (ใช้ zoning_code = next_zone ตาม Python)
	base, _ := url.Parse(h.cfg.ServerURL)
	base.Path = path.Join(base.Path, "/api/v1-202402/zoning/transition")

	reqBody := map[string]any{
		"license_plate":   plate,
		"parking_code":    h.cfg.ParkingCode,
		"zoning_code":     nextZone,
		"vehicle_type_id": utils.VehicleType(vehicleType),
		"gate_id":         gateNo,
	}
	transitionURL := base.String()

	resData, err := h.postJSON(transitionURL, reqBody)
	if err != nil {
		log.Printf("[transition][EXT] error: %v", err)
		c.String(http.StatusBadGateway, "transition failed")
		return
	}
	t4 := time.Since(t0) - t1 - t2 - t3

	// เติมรูปป้ายเข้า data
	if resData != nil {
		if _, ok := resData["data"]; !ok {
			resData["data"] = map[string]any{}
		}
		if dm, ok := resData["data"].(map[string]any); ok {
			dm["license_plate_img_base64"] = base64.StdEncoding.EncodeToString(lpImg)
		}
	}

	// ถ้า success → PUT collect-image (payload ใช้ zoning_code เดิม + "gate":"ent" ตาม Python)
	if resData != nil && h.boolish(resData["status"]) {
		u := h.getUUIDFromData(resData)

		collectBase, _ := url.Parse(h.cfg.ServerURL)
		collectBase.Path = path.Join(collectBase.Path, "/api/v1-202402/zoning/collect-image/", u)
		collectURL := collectBase.String()

		payload := map[string]any{
			"license_plate":            plate,
			"park_code":                h.cfg.ParkingCode,
			"zoning_code":              zoningCode, // Python เดิมใช้ค่าตัวนี้ (ไม่ใช่ next_zone)
			"time_stamp":               time.Now().Format(time.RFC3339),
			"gate":                     "ent", // คงค่าตามต้นฉบับ
			"license_plate_img_base64": base64.StdEncoding.EncodeToString(lpImg),
			"driver_img_base_64":       base64.StdEncoding.EncodeToString(dtImg),
		}
		go func() {
			_, err := h.putJSON(collectURL, payload)
			if err != nil {
				log.Printf("[collect-image][EXT] err: %v", err)
			}
		}()
	}
	t5 := time.Since(t0) - t1 - t2 - t3 - t4

	// Step 5: broadcast
	h.broadcastJSON(room, resData)
	t6 := time.Since(t0) - t1 - t2 - t3 - t4 - t5

	gateStr := c.Query("gate_no") // "01", "1", ...
	gateNoE, err := strconv.Atoi(gateStr)
	if err != nil {
		// ถ้าอยาก safety หน่อย:
		log.Printf("invalid gate_no %q: %v", gateStr, err)
		c.String(http.StatusBadRequest, "invalid gate_no")
		return
	}

	// ENTRANCE
	envKey := fmt.Sprintf("HIK_LED_ZONE_EXT_%02d", gateNoE)
	// EXIT
	// envKey := fmt.Sprintf("HIK_LED_ZONE_EXT_%02d", gateNo)

	value, ok := os.LookupEnv(envKey) // ช่วยแยก "ไม่ได้ตั้ง" กับ "ตั้งแต่ค่าว่าง"
	if !ok || value == "" {
		log.Printf("Environment variable %s not found or empty", envKey)
		// จะ return เลยหรือข้ามการแสดง LED ไปก็ได้ แล้วทำงานต่อส่วนอื่น
		// return
	}

	// (Optional) แสดง LED
	disErr := utils.DisplayHexData(
		value,                    // screen_ip
		9999,                     // screen_port
		plate,                    // license_plate
		"ext",                    // direction
		"zone",                   // state_type
		fmt.Sprintf("%d THB", 0), // line3
	)
	if disErr != nil {
		fmt.Println("Error:", disErr)
	} else {
		fmt.Println("Packet sent successfully.")
	}

	// Log timing
	h.logZoningTiming("EXT", zoningCode, gateNo, plate, t1, t2, t3, t4, t5, t6, 0, t0)

	c.String(http.StatusOK, "File(s) uploaded successfully")
}

// ------------------------------------------------------------
// putJSON: เพิ่มเติมสำหรับ collect-image (pattern เดียวกับ post/get)
// ------------------------------------------------------------

func (h *Handler) putJSON(url string, body map[string]any) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, url, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out, nil
}

// ------------------------------------------------------------
// Logging ให้หน้าตาคล้ายของ order.*
// ------------------------------------------------------------

func (h *Handler) logZoningTiming(dir, zoning, gate, plate string,
	t1, t2, t3, t4, t5, t6, t7 time.Duration, t0 time.Time) {

	tag := "ZONING " + dir
	log.Printf("[%s %s:%s, LICENSE PLATE: %s]", tag, zoning, gate, plate)
	log.Printf(" - Parse Multipart:  %.2fs", t1.Seconds())
	log.Printf(" - Parse XML:        %.2fs", t2.Seconds())
	log.Printf(" - plpPost:          %.2fs", t3.Seconds())
	if dir == "ENT" {
		log.Printf(" - Transition(ent):  %.2fs", t4.Seconds())
	} else {
		log.Printf(" - Transition(ext):  %.2fs", t4.Seconds())
	}
	log.Printf(" - CollectImage BG:  %.2fs", t5.Seconds())
	log.Printf(" - Broadcast:        %.2fs", t6.Seconds())
	if t7 > 0 {
		log.Printf(" - Extra:            %.2fs", t7.Seconds())
	}
	log.Printf(" - Total Time:       %.2fs", time.Since(t0).Seconds())
}

// ----------------- helpers -----------------
func (h *Handler) postParkingLicensePlate(plate, ip, code string) {
	url := fmt.Sprintf("%s/api/v1-202402/order/parking_license_plate", h.cfg.ServerURL)
	body := map[string]any{
		"license_plate": plate,
		"ip_address":    ip,
		"parking_code":  code,
	}
	if _, err := h.postJSON(url, body); err != nil {
		log.Println("Background task error:", err)
	}
}

func (h *Handler) postJSON(url string, body map[string]any) (map[string]any, error) {
	b, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out, nil
}

func (h *Handler) broadcastJSON(room string, payload map[string]any) {
	b, _ := json.Marshal(payload)
	h.hub.Broadcast(room, b)
}
