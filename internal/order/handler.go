package order

import (
	"bytes"
	"context"
	"crypto/tls"
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

	digest "github.com/icholy/digest"

	"GO_LANG_WORKSPACE/internal/config"
	"GO_LANG_WORKSPACE/internal/utils"
	"GO_LANG_WORKSPACE/internal/ws"

	"github.com/gin-gonic/gin"
)

// Handler หลัก
type Handler struct {
	cfg        *config.Config
	hub        *ws.Hub
	httpClient *http.Client // ไว้ยิง Cloud (transport ปกติ)
	camClient  *http.Client // ไว้ยิงกล้อง (Digest)
	deduper    *utils.Deduper
}

func NewHandler(cfg *config.Config, hub *ws.Hub) *Handler {
	// client สำหรับ Cloud / API ภายนอก
	httpCli := &http.Client{
		Timeout:   6 * time.Second,
		Transport: config.NewHTTPTransport(),
	}

	// client สำหรับกล้อง (Digest)
	camTr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: false}, // ถ้ากล้อง self-signed ค่อยปรับเป็น true
	}
	camDT := &digest.Transport{
		Username:  cfg.CameraUser,
		Password:  cfg.CameraPass,
		Transport: camTr,
	}
	camCli := &http.Client{
		Timeout:   5 * time.Second, // อิง SNAPSHOT_TIMEOUT_MS ~ 5000
		Transport: camDT,
	}

	return &Handler{
		cfg:        cfg,
		hub:        hub,
		httpClient: httpCli,
		camClient:  camCli,
		deduper:    utils.NewDeduper(30 * time.Second),
	}
}

func (h *Handler) VerifyMember(c *gin.Context) {
	t0 := time.Now()

	// Step 1: Validate Request
	ct := c.GetHeader("Content-Type")
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		c.String(http.StatusBadRequest, "Invalid request")
		return
	}
	mr := multipart.NewReader(c.Request.Body, params["boundary"])
	t1 := time.Since(t0)

	// Step 2: Separate Files (XML + licensePlatePicture.jpg; อื่น ๆ ทิ้ง)
	var xmlBuf []byte
	var lpImg []byte
	var _ []byte // ฝั่งนี้ไม่ใช้ (detected/pedestrian)

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		fn := part.FileName()
		if fn == "" {
			// form field (ไม่ใช่ไฟล์)
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
			_ = buf
		default:
		}
	}

	if len(xmlBuf) == 0 {
		c.String(http.StatusBadRequest, "Missing XML file")
		return
	}
	t2 := time.Since(t0) - t1

	// Step 3: Parse XML (มี namespace ก่อน → fallback)
	var plate, uuid, timeIn, ip, vehicleType string
	{
		var ev eventXML
		if err := xml.Unmarshal(xmlBuf, &ev); err == nil && strings.TrimSpace(ev.ANPR.LicensePlate) != "" {
			plate = strings.TrimSpace(ev.ANPR.LicensePlate)
			uuid = strings.TrimSpace(ev.UUID)
			timeIn = strings.TrimSpace(ev.DateTime)
			ip = strings.TrimSpace(ev.IPAddress)
			vehicleType = strings.TrimSpace(ev.ANPR.VehicleType)
		} else {
			var ev2 eventXMLNoNS
			if err2 := xml.Unmarshal(xmlBuf, &ev2); err2 == nil && strings.TrimSpace(ev2.ANPR.LicensePlate) != "" {
				plate = strings.TrimSpace(ev2.ANPR.LicensePlate)
				uuid = strings.TrimSpace(ev2.UUID)
				timeIn = strings.TrimSpace(ev2.DateTime)
				ip = strings.TrimSpace(ev2.IPAddress)
				// NOTE: คงตรรกะเดิมเป๊ะ ๆ (อ้าง ev.ANPR.VehicleType) — ไม่แก้ให้ถูกต้องตามสัญชาตญาณ
				vehicleType = strings.TrimSpace(ev.ANPR.VehicleType)
			}
		}
	}
	if plate == "" {
		c.String(http.StatusBadRequest, "Failed to parse XML")
		return
	}
	t3 := time.Since(t0) - t1 - t2

	// Step 4: De-dup (ยังคงคอมเมนต์ไว้ตามโค้ดเดิม)
	gateNo := c.Query("gate_no")
	// key := fmt.Sprintf("%s|%s|in", plate, gateNo)
	// if h.deduper.Hit(key) {
	// 	log.Println("[Step4][De-dup] duplicated within TTL")
	// 	c.JSON(http.StatusOK, gin.H{"detail": "Duplicate license plate detected within TTL"})
	// 	// บันทึก timing แบบข้ามส่วนที่เหลือ
	// 	h.logTimings(c, t0, t1, t2, t3, 0, 0, 0, 0, plate)
	// 	return
	// }
	t4 := time.Since(t0) - t1 - t2 - t3

	// Step 5: Background Save (postParkingLicensePlate)
	go h.postParkingLicensePlate(plate, ip, h.cfg.ParkingCode)
	t5 := time.Since(t0) - t1 - t2 - t3 - t4

	// Step 6: Call Cloud (get-customer-id)
	base, _ := url.Parse(h.cfg.ServerURL)
	base.Path = path.Join(base.Path, "/api/v2-202402/order/get-customer-id")

	q := base.Query()
	q.Set("license_plate", plate)
	q.Set("parking_code", h.cfg.ParkingCode)
	base.RawQuery = q.Encode()

	exitURL := base.String()

	jsonRes, err := h.getJSON(exitURL)
	if err != nil {
		log.Printf("[Step6][cloud] error: %v", err)
	}
	var custID, efID any
	if jsonRes != nil {
		custID = jsonRes["cust_id"]
		efID = jsonRes["ef_id"]
	}
	t6 := time.Since(t0) - t1 - t2 - t3 - t4 - t5

	// Step 7: Broadcast WebSocket
	payload := map[string]any{
		"license_plate":            plate,
		"uuid":                     uuid,
		"time_in":                  timeIn,
		"cust_id":                  custID,
		"ef_id":                    efID,
		"vehicle_type":             utils.VehicleType(vehicleType), // คงตรรกะเดิม (default 1 ถ้าโล่ง)
		"license_plate_img_base64": base64.StdEncoding.EncodeToString(lpImg),
	}
	room := "gate_in_" + gateNo
	h.broadcastJSON(room, payload)
	t7 := time.Since(t0) - t1 - t2 - t3 - t4 - t5 - t6

	// รวม timing + ปิดจ๊อบ (ใช้รูปแบบเดียวกับฝั่ง OUT)
	h.logTimingsEntrance(c, t0, t1, t2, t3, t4, t5, t6, t7, plate)
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
	var _ []byte
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

		// จับจากนามสกุลง่าย ๆ
		switch {
		case strings.HasSuffix(strings.ToLower(fn), ".xml"):
			xmlBuf = buf
		case fn == "licensePlatePicture.jpg":
			_ = buf
		case fn == "detectedImage.jpg" || fn == "pedestrianDetectionPicture.jpg":
			_ = buf
		}
	}
	if len(xmlBuf) == 0 {
		c.String(http.StatusBadRequest, "Missing XML file")
		return
	}
	t2 := time.Since(t0) - t1

	// Step 3: Parse XML (ลองมี-namespace ก่อน, แล้ว fallback)
	// Step 3: Parse XML (มี namespace ก่อน → fallback)
	var plate, _, _, ip, _ string
	{
		var ev eventXML
		if err := xml.Unmarshal(xmlBuf, &ev); err == nil && strings.TrimSpace(ev.ANPR.LicensePlate) != "" {
			plate = strings.TrimSpace(ev.ANPR.LicensePlate)
			_ = strings.TrimSpace(ev.UUID)
			_ = strings.TrimSpace(ev.DateTime)
			ip = strings.TrimSpace(ev.IPAddress)
			_ = strings.TrimSpace(ev.ANPR.VehicleType)
		} else {
			var ev2 eventXMLNoNS
			if err2 := xml.Unmarshal(xmlBuf, &ev2); err2 == nil && strings.TrimSpace(ev2.ANPR.LicensePlate) != "" {
				plate = strings.TrimSpace(ev2.ANPR.LicensePlate)
				_ = strings.TrimSpace(ev2.UUID)
				_ = strings.TrimSpace(ev2.DateTime)
				ip = strings.TrimSpace(ev2.IPAddress)
				// NOTE: คงตรรกะเดิมเป๊ะ ๆ (อ้าง ev.ANPR.VehicleType) — ไม่แก้ให้ถูกต้องตามสัญชาตญาณ
				_ = strings.TrimSpace(ev.ANPR.VehicleType)
			}
		}
	}
	if plate == "" {
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
		h.logTimingsExit(c, t0, t1, t2, t3, t4, t5, 0, 0, plate) // ยังไม่ดึงรูป/WS
		return
	}

	// Step 6: Fetch images (ดึงพร้อมกันแบบ concurrent)
	//utils.FetchImagesConcurrently จะอ่าน env URL แล้วคืน base64 ของรูปที่ได้
	images := utils.FetchImagesHedgeHosts(h.cfg, c.Query("gate_no"), h.camClient)

	//var lpB64, dtB64 string
	//if len(lpImg) > 0 {
	//	lpB64 = base64.StdEncoding.EncodeToString(lpImg)
	//}
	//if len(dtImg) > 0 {
	//	dtB64 = base64.StdEncoding.EncodeToString(dtImg)
	//}

	t6 := time.Since(t0) - t1 - t2 - t3 - t4 - t5

	// Step 7: Broadcast WebSocket
	// รวมผล cloud + รูป ส่งไปห้อง gate_out_{gate_no}
	broadcast := make(map[string]any)
	for k, v := range jsonRes {
		broadcast[k] = v
	}
	//if lpB64 != "" {
	//	broadcast["lpr_out"] = lpB64
	//}
	//if dtB64 != "" {
	//	broadcast["license_plate_out"] = dtB64
	//}
	for k, v := range images {
		broadcast[k] = v
	}

	room := "gate_out_" + c.Query("gate_no")
	h.broadcastJSON(room, broadcast)
	t7 := time.Since(t0) - t1 - t2 - t3 - t4 - t5 - t6

	// (Optional) แสดง LED
	_ = toPay // ใช้กับจอ LED ได้ตามจริง

	// Log timings + total
	h.logTimingsExit(c, t0, t1, t2, t3, t4, t5, t6, t7, plate)

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

func (h *Handler) logTimingsExit(c *gin.Context, t0 time.Time, t1, t2, t3, t4, t5, t6, t7 time.Duration, plate string) {
	log.Printf("[GATE OUT %s: LICENSE PLATE: %s]", c.Query("gate_no"), plate)
	log.Printf(" - Parse Multipart:  %.2fs", t1.Seconds())
	log.Printf(" - Split Files:      %.2fs", t2.Seconds())
	log.Printf(" - Parse XML:        %.2fs", t3.Seconds())
	log.Printf(" - plpPost:          %.2fs", t4.Seconds())
	log.Printf(" - Call API:         %.2fs", t5.Seconds())
	log.Printf(" - Fetch Images:     %.2fs", t6.Seconds())
	log.Printf(" - Broadcast:        %.2fs", t7.Seconds())
	log.Printf(" - Total Time:       %.2fs", time.Since(t0).Seconds())
}

func (h *Handler) logTimingsEntrance(c *gin.Context, t0 time.Time, t1, t2, t3, t4, t5, t6, t7 time.Duration, plate string) {
	log.Printf("[GATE IN %s: LICENSE PLATE: %s]", c.Query("gate_no"), plate)
	log.Printf(" - Parse Multipart:  %.2fs", t1.Seconds())
	log.Printf(" - Split Files:      %.2fs", t2.Seconds())
	log.Printf(" - Parse XML:        %.2fs", t3.Seconds())
	log.Printf(" - De-dup:           %.2fs", t4.Seconds())
	log.Printf(" - plpPost:          %.2fs", t5.Seconds())
	log.Printf(" - Call API:         %.2fs", t6.Seconds())
	log.Printf(" - Broadcast:        %.2fs", t7.Seconds())
	log.Printf(" - Total Time:       %.2fs", time.Since(t0).Seconds())
}
