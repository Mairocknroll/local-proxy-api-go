package order

import (
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
	"path"
	"strings"
	"time"

	"GO_LANG_WORKSPACE/internal/config"
	"GO_LANG_WORKSPACE/internal/utils"
	"GO_LANG_WORKSPACE/internal/ws"

	"github.com/gin-gonic/gin"
)

// ---------- XML models ----------

// แบบมี namespace (Hikvision ปกติ)
type eventXML struct {
	XMLName   xml.Name `xml:"http://www.isapi.org/ver20/XMLSchema EventNotificationAlert"`
	IPAddress string   `xml:"ipAddress"`
	DateTime  string   `xml:"dateTime"`
	UUID      string   `xml:"UUID"`
	ANPR      struct {
		VehicleType  string `xml:"vehicleType"`
		LicensePlate string `xml:"licensePlate"`
	} `xml:"ANPR"`
}

// แบบไม่มี namespace (fallback)
type eventXMLNoNS struct {
	XMLName   xml.Name `xml:"EventNotificationAlert"`
	IPAddress string   `xml:"ipAddress"`
	DateTime  string   `xml:"dateTime"`
	UUID      string   `xml:"UUID"`
	ANPR      struct {
		VehicleType  string `xml:"vehicleType"`
		LicensePlate string `xml:"licensePlate"`
	} `xml:"ANPR"`
}

// Handler หลัก
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

// POST /api/v2-202402/order/verify-member?gate_no=1
func (h *Handler) VerifyMember(c *gin.Context) {
	start := time.Now()

	// 1) validate content-type
	ct := c.GetHeader("Content-Type")
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		c.String(http.StatusBadRequest, "Invalid request")
		return
	}
	mr := multipart.NewReader(c.Request.Body, params["boundary"])

	// 2) แยกไฟล์ที่ต้องใช้ (XML + licensePlatePicture.jpg)
	var xmlBuf []byte
	var lpImg []byte
	var _ []byte

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
		case fn == "detectedImage.jpg":
			_ = buf
		}
	}

	if len(xmlBuf) == 0 {
		c.String(http.StatusBadRequest, "Missing XML file")
		return
	}

	// 3) parse XML (namespace → fallback)
	var plate, uuid, timeIn, _, vehicleType string
	{
		var ev eventXML
		if err := xml.Unmarshal(xmlBuf, &ev); err == nil && strings.TrimSpace(ev.ANPR.LicensePlate) != "" {
			plate = strings.TrimSpace(ev.ANPR.LicensePlate)
			uuid = strings.TrimSpace(ev.UUID)
			timeIn = strings.TrimSpace(ev.DateTime)
			_ = strings.TrimSpace(ev.IPAddress)
			vehicleType = strings.TrimSpace(ev.ANPR.VehicleType)
		} else {
			var ev2 eventXMLNoNS
			if err2 := xml.Unmarshal(xmlBuf, &ev2); err2 == nil && strings.TrimSpace(ev2.ANPR.LicensePlate) != "" {
				plate = strings.TrimSpace(ev2.ANPR.LicensePlate)
				uuid = strings.TrimSpace(ev2.UUID)
				timeIn = strings.TrimSpace(ev2.DateTime)
				_ = strings.TrimSpace(ev2.IPAddress)
				vehicleType = strings.TrimSpace(ev.ANPR.VehicleType)
			}
		}
	}
	if plate == "" {
		log.Println("[XML Error] cannot find ANPR.licensePlate")
		c.String(http.StatusBadRequest, "Failed to parse XML")
		return
	}

	// 4) de-dup
	gateNo := c.Query("gate_no")
	//key := fmt.Sprintf("%s|%s|in", plate, gateNo)
	//if h.deduper.Hit(key) {
	//	log.Println("Duplicated ")
	//	c.JSON(http.StatusOK, gin.H{"detail": "Duplicate license plate detected within TTL"})
	//	return
	//}

	// 5) background: แจ้งขึ้น Cloud ว่ามี plate + ip (เหมือน Python)
	//go h.postParkingLicensePlate(plate, ip, h.cfg.ParkingCode)

	// 6) ดึง customer info จาก Cloud
	getURL := fmt.Sprintf("%s/api/v1-202402/order/get-customer-id?license_plate=%s&parking_code=%s",
		h.cfg.ServerURL, plate, h.cfg.ParkingCode)
	resData, _ := h.getJSON(getURL)
	var custID, efID any
	if resData != nil {
		custID = resData["cust_id"]
		efID = resData["ef_id"]
	}

	// 7) เตรียม payload เท่าที่กำหนด
	payload := map[string]any{
		"license_plate":            plate,
		"uuid":                     uuid,
		"time_in":                  timeIn,
		"cust_id":                  custID,
		"ef_id":                    efID,
		"vehicle_type":             utils.VehicleType(vehicleType), // ไม่มีใน XML → default 1
		"license_plate_img_base64": base64.StdEncoding.EncodeToString(lpImg),
	}
	room := "gate_in_" + gateNo
	h.broadcastJSON(room, payload)

	log.Printf("[GATE IN %s] LP=%s total=%.2fs", gateNo, plate, time.Since(start).Seconds())
	c.String(http.StatusOK, "File(s) uploaded successfully")
}

// VerifyLicensePlateOut = POST /api/v2-202402/order/verify-license-plate-out?gate_no=1
func (h *Handler) VerifyLicensePlateOut(c *gin.Context) {
	t0 := time.Now()

	// Step 1: Validate Request
	ct := c.GetHeader("Content-Type")
	mediatype, params, err := mime.ParseMediaType(ct)
	if err != nil || !strings.HasPrefix(mediatype, "multipart/") {
		c.String(http.StatusBadRequest, "Invalid request")
		return
	}
	mr := multipart.NewReader(c.Request.Body, params["boundary"])
	t1 := time.Since(t0)

	// Step 2: Separate Files (ดึงเฉพาะ XML; รูปไม่จำเป็นก็ข้ามได้)
	var xmlBuf []byte
	var lpImg []byte
	var dtImg []byte
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

		// จับจากนามสกุลง่าย ๆ
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
		return
	}
	t2 := time.Since(t0) - t1

	// Step 3: Parse XML (ลองมี-namespace ก่อน, แล้ว fallback)
	var ip, plate string
	{
		var ev eventXML
		if err := xml.Unmarshal(xmlBuf, &ev); err == nil && strings.TrimSpace(ev.ANPR.LicensePlate) != "" {
			plate = strings.TrimSpace(ev.ANPR.LicensePlate)
			ip = ev.IPAddress
		} else {
			var ev2 eventXMLNoNS
			if err2 := xml.Unmarshal(xmlBuf, &ev2); err2 == nil && strings.TrimSpace(ev2.ANPR.LicensePlate) != "" {
				plate = strings.TrimSpace(ev2.ANPR.LicensePlate)
				ip = ev2.IPAddress
			}
		}
	}
	if plate == "" {
		log.Println("[XML Parse Error] cannot find ANPR.licensePlate")
		c.String(http.StatusBadRequest, "Failed to parse XML")
		return
	}
	t3 := time.Since(t0) - t1 - t2

	// Step 4: Background Save (เหมือน Python post_parking_license_plate)
	go h.postParkingLicensePlate(plate, ip, h.cfg.ParkingCode)
	t4 := time.Since(t0) - t1 - t2 - t3

	// Step 5: Call valet/exit check บน Cloud
	base, _ := url.Parse(h.cfg.ServerURL)
	base.Path = path.Join(base.Path, "/api/v1-202402/order/license-plate-exit")

	q := base.Query()
	q.Set("license_plate", plate)
	q.Set("parking_code", h.cfg.ParkingCode)
	base.RawQuery = q.Encode()

	exitURL := base.String()

	jsonRes, err := h.getJSON(exitURL)
	if err != nil {
		log.Printf("[cloud] error: %v", err)
	}
	t5 := time.Since(t0) - t1 - t2 - t3 - t4

	// แยกค่า to_pay_amount (ถ้ามี)
	var toPay any
	if jsonRes != nil && jsonRes["status"] == false {
		if data, ok := jsonRes["data"].(map[string]any); ok {
			toPay = data["to_pay_amount"]
		}
	}

	// valet auto-open
	if jsonRes != nil && jsonRes["status"] == true && jsonRes["message"] == "valet user" {
		log.Printf("valet user exit %s", plate)
		// TODO: เรียก barrier control ของจริงตรงนี้ได้
		c.Status(http.StatusOK)
		h.logTimings(c, t0, t1, t2, t3, t4, t5, 0, 0, plate) // ยังไม่ดึงรูป/WS
		return
	}

	// Step 6: Fetch images (ดึงพร้อมกันแบบ concurrent)
	// utils.FetchImagesConcurrently จะอ่าน env URL แล้วคืน base64 ของรูปที่ได้
	//images := utils.FetchImagesConcurrently(h.cfg, c.Query("gate_no"))

	var lpB64, dtB64 string
	if len(lpImg) > 0 {
		lpB64 = base64.StdEncoding.EncodeToString(lpImg)
	}
	if len(dtImg) > 0 {
		dtB64 = base64.StdEncoding.EncodeToString(dtImg)
	}

	t6 := time.Since(t0) - t1 - t2 - t3 - t4 - t5

	// Step 7: Broadcast WebSocket
	// รวมผล cloud + รูป ส่งไปห้อง gate_out_{gate_no}
	broadcast := make(map[string]any)
	for k, v := range jsonRes {
		broadcast[k] = v
	}
	if lpB64 != "" {
		broadcast["lpr_out"] = lpB64
	}
	if dtB64 != "" {
		broadcast["license_plate_out"] = dtB64
	}
	//for k, v := range images {
	//	broadcast[k] = v
	//}

	room := "gate_out_" + c.Query("gate_no")
	h.broadcastJSON(room, broadcast)
	t7 := time.Since(t0) - t1 - t2 - t3 - t4 - t5 - t6

	// (Optional) แสดง LED
	_ = toPay // ใช้กับจอ LED ได้ตามจริง

	// Log timings + total
	h.logTimings(c, t0, t1, t2, t3, t4, t5, t6, t7, plate)

	c.String(http.StatusOK, "File(s) uploaded successfully")
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

func (h *Handler) getJSON(url string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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

func (h *Handler) logTimings(c *gin.Context, t0 time.Time, t1, t2, t3, t4, t5, t6, t7 time.Duration, plate string) {
	log.Printf("[GATE OUT %s: LICENSE PLATE: %s]", c.Query("gate_no"), plate)
	log.Printf(" - Parse Multipart: %.2fs", t1.Seconds())
	log.Printf(" - Split Files:     %.2fs", t2.Seconds())
	log.Printf(" - Parse XML:       %.2fs", t3.Seconds())
	log.Printf(" - Save Task:       %.2fs", t4.Seconds())
	log.Printf(" - Call API:        %.2fs", t5.Seconds())
	log.Printf(" - Fetch Images:    %.2fs", t6.Seconds())
	log.Printf(" - Broadcast:       %.2fs", t7.Seconds())
	log.Printf(" - Total Time:      %.2fs", time.Since(t0).Seconds())
}
