package order

import (
	"bytes"
	"context"
	"crypto/tls"
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

	digest "github.com/icholy/digest"
	"golang.org/x/sync/errgroup"

	"GO_LANG_WORKSPACE/internal/config"
	"GO_LANG_WORKSPACE/internal/utils"
	"GO_LANG_WORKSPACE/internal/ws"

	"github.com/gin-gonic/gin"

	"path/filepath"
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

	// ---------- Step 1: Validate Request + limit body ----------
	ct := c.GetHeader("Content-Type")
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		c.String(http.StatusBadRequest, "Invalid request")
		return
	}

	// กัน body ใหญ่เกิน (เช่น กล้องส่งรูปบวม/ผิดพลาด) — ป้องกันค้างยาว
	const maxBody = int64(16 << 20) // 16MB รวมทั้ง multipart
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBody)
	defer c.Request.Body.Close()

	mr := multipart.NewReader(c.Request.Body, params["boundary"])
	t1 := time.Since(t0)

	// ---------- Step 2: Separate Files ----------
	var xmlBuf []byte
	var lpImg []byte
	var detectImg = []byte(nil) // detected/pedestrian: ไม่ใช้

	parts := 0
	const maxParts = 10 // กันลูปไม่รู้จบจาก input แปลก

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			// เจอ error ระหว่างอ่าน (รวม timeout) → จบรีเควสต์ทันที
			// ใช้ 408 ถ้าดูเหมือน timeout, นอกนั้น 400
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				c.String(http.StatusRequestTimeout, "multipart read timeout")
			} else {
				c.String(http.StatusBadRequest, "invalid multipart")
			}
			return
		}

		parts++
		if parts > maxParts {
			_ = part.Close()
			c.String(http.StatusBadRequest, "too many parts")
			return
		}

		fn := part.FileName()
		if fn == "" {
			// form field (ไม่ใช่ไฟล์) — ทิ้ง
			_, _ = io.Copy(io.Discard, part)
			_ = part.Close()
			continue
		}

		// อ่านทั้งไฟล์เข้าหน่วยความจำ (คงพฤติกรรมเดิม)
		buf, _ := io.ReadAll(part)
		_ = part.Close()

		switch {
		case strings.HasSuffix(strings.ToLower(fn), ".xml"):
			xmlBuf = buf
		case fn == "licensePlatePicture.jpg":
			lpImg = buf
		case fn == "detectedImage.jpg" || fn == "pedestrianDetectionPicture.jpg":
			detectImg = buf
		default:
			// ไฟล์อื่นไม่รู้จัก — ทิ้ง
		}
	}

	if len(xmlBuf) == 0 {
		c.String(http.StatusBadRequest, "Missing XML file")
		return
	}
	t2 := time.Since(t0) - t1

	// ---------- Step 3: Parse XML (คงตรรกะเดิม) ----------
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
				// คงตรรกะเดิมตามคอมเมนต์ของคุณ
				vehicleType = strings.TrimSpace(ev.ANPR.VehicleType)
			}
		}
	}
	if plate == "" {
		c.String(http.StatusBadRequest, "Failed to parse XML")
		return
	}
	t3 := time.Since(t0) - t1 - t2

	// ---------- Step 4.. ต่อจากนี้เหมือนเดิม ----------
	gateNo := c.Query("gate_no")
	t4 := time.Since(t0) - t1 - t2 - t3

	go h.postParkingLicensePlate(plate, ip, h.cfg.ParkingCode)
	t5 := time.Since(t0) - t1 - t2 - t3 - t4

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

	gateStr := c.Query("gate_no")
	gateNoE, err := strconv.Atoi(gateStr)
	if err != nil {
		log.Printf("invalid gate_no %q: %v", gateStr, err)
		c.String(http.StatusBadRequest, "invalid gate_no")
		return
	}

	envKey := fmt.Sprintf("HIK_LED_MAIN_ENT_%02d", gateNoE)
	value, ok := os.LookupEnv(envKey)
	if !ok || value == "" {
		log.Printf("Environment variable %s not found or empty", envKey)
	}

	disErr := utils.DisplayHexData(value, 9999, plate, "ent", "main", "")
	if disErr != nil {
		fmt.Println("Error:", disErr)
	} else {
		fmt.Println("Packet sent successfully.")
	}

	if lpImg == nil {
		lpImg = detectImg
	}

	log.Printf("🔍 Debug: lpImg == nil? %v", lpImg == nil)
	log.Printf("🔍 Debug: lpImg length: %d bytes", len(lpImg))

	var imageURL string
	if len(lpImg) > 0 {
		log.Printf("📸 Starting image save process...")

		uploadDir := "./uploads"
		log.Printf("📁 Upload dir: %s", uploadDir)

		if err := os.MkdirAll(uploadDir, os.ModePerm); err != nil {
			log.Printf("❌ Failed to create directory: %v", err)
		} else {
			log.Printf("✅ Directory created/exists")
		}

		timestamp := time.Now().Format("20060102_150405")
		safePlate := strings.ReplaceAll(plate, "-", "")
		safePlate = strings.ReplaceAll(safePlate, " ", "")
		filename := fmt.Sprintf("plate_%s_%s.jpg", timestamp, safePlate)
		imagePath := filepath.Join(uploadDir, filename)

		log.Printf("💾 Attempting to save: %s", imagePath)

		if err := os.WriteFile(imagePath, lpImg, 0644); err != nil {
			log.Printf("❌ Failed to save image: %v", err)
		} else {
			log.Printf("✅ Image saved: %s", filename)
			log.Printf("✅ Saved at: %s", imagePath)

			imageURL = fmt.Sprintf("%s/images/%s", h.cfg.SelfHost, filename)
			log.Printf("✅ Image URL: %s", imageURL)
		}
	} else {
		log.Printf("⚠️ lpImg is empty, skipping save")
	}

	// Payload
	payload := map[string]any{
		"license_plate": plate,
		"uuid":          uuid,
		"time_in":       timeIn,
		"cust_id":       custID,
		"ef_id":         efID,
		"vehicle_type":  utils.VehicleType(vehicleType),
	}

	if imageURL != "" {
		payload["license_plate_img_url"] = imageURL
	}

	room := "gate_in_" + gateNo
	h.broadcastJSON(room, payload)
	t7 := time.Since(t0) - t1 - t2 - t3 - t4 - t5 - t6

	h.logTimingsEntrance(c, t0, t1, t2, t3, t4, t5, t6, t7, plate)
	c.String(http.StatusOK, "File(s) uploaded successfully")
}

// VerifyLicensePlateOut = POST /api/v2-202402/order/verify-license-plate-out?gate_no=1
// VerifyLicensePlateOut = POST /api/v2-202402/order/verify-license-plate-out?gate_no=1
func (h *Handler) VerifyLicensePlateOut(c *gin.Context) {
	ct := c.GetHeader("Content-Type")
	mediatype, params, err := mime.ParseMediaType(ct)
	if err != nil || !strings.HasPrefix(mediatype, "multipart/") {
		c.String(http.StatusBadRequest, "Invalid request")
		return
	}

	const maxBody = int64(16 << 20)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBody)
	defer c.Request.Body.Close()

	mr := multipart.NewReader(c.Request.Body, params["boundary"])

	// ------------------- read files once -------------------
	var xmlBuf []byte
	var lpImg []byte
	var detectImg []byte

	parts := 0
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			c.String(http.StatusBadRequest, "multipart read error")
			return
		}
		parts++
		if parts > 10 {
			c.String(http.StatusBadRequest, "too many parts")
			return
		}
		fn := part.FileName()
		if fn == "" {
			io.Copy(io.Discard, part)
			part.Close()
			continue
		}
		buf, _ := io.ReadAll(part)
		part.Close()

		switch strings.ToLower(fn) {
		case "licenseplatepicture.jpg":
			lpImg = buf
		case "detectedimage.jpg", "pedestriandetectionpicture.jpg":
			detectImg = buf
		default:
			if strings.HasSuffix(strings.ToLower(fn), ".xml") {
				xmlBuf = buf
			}
		}
	}

	if len(xmlBuf) == 0 {
		c.String(http.StatusBadRequest, "missing xml")
		return
	}

	// ------------------- parse xml -------------------
	var plate, ip string
	{
		var ev eventXML
		if err := xml.Unmarshal(xmlBuf, &ev); err == nil && strings.TrimSpace(ev.ANPR.LicensePlate) != "" {
			plate = strings.TrimSpace(ev.ANPR.LicensePlate)
			ip = strings.TrimSpace(ev.IPAddress)
		} else {
			var ev2 eventXMLNoNS
			if err2 := xml.Unmarshal(xmlBuf, &ev2); err2 == nil && strings.TrimSpace(ev2.ANPR.LicensePlate) != "" {
				plate = strings.TrimSpace(ev2.ANPR.LicensePlate)
				ip = strings.TrimSpace(ev2.IPAddress)
			}
		}
	}
	if plate == "" {
		c.String(http.StatusBadRequest, "xml parse fail")
		return
	}

	ts := time.Now().Format("20060102_150405")
	safePlate := strings.NewReplacer("-", "", " ", "").Replace(plate)

	filenameLic := fmt.Sprintf("plate_%s_lic_%s.jpg", ts, safePlate)
	filenameDet := fmt.Sprintf("plate_%s_det_%s.jpg", ts, safePlate)

	fullpathLic := filepath.Join("./uploads", filenameLic)
	fullpathDet := filepath.Join("./uploads", filenameDet)

	// BG post
	go h.postParkingLicensePlate(plate, ip, h.cfg.ParkingCode)

	// ------------------- parallel section -------------------
	ctx, cancel := context.WithTimeout(c, 3*time.Second)
	defer cancel()

	var jsonRes map[string]any

	g, _ := errgroup.WithContext(ctx)

	// call cloud
	g.Go(func() error {
		base, _ := url.Parse(h.cfg.ServerURL)
		base.Path = path.Join(base.Path, "/api/v1-202402/order/license-plate-exit")
		q := base.Query()
		q.Set("license_plate", plate)
		q.Set("parking_code", h.cfg.ParkingCode)
		base.RawQuery = q.Encode()
		res, err := h.getJSON(base.String())
		if err != nil {
			jsonRes = map[string]any{"status": false, "message": "cloud error"}
			return nil
		}
		jsonRes = res
		return nil
	})

	// save plate image
	g.Go(func() error {
		if len(lpImg) == 0 {
			return nil
		}
		os.MkdirAll("./uploads", 0755)
		return os.WriteFile(fullpathLic, lpImg, 0644)
	})

	g.Go(func() error {
		if len(detectImg) == 0 {
			return nil
		}
		os.MkdirAll("./uploads", 0755)
		return os.WriteFile(fullpathDet, detectImg, 0644)
	})

	g.Wait()

	// attach URLs ( prefix: /images/* )
	if len(lpImg) > 0 {
		jsonRes["license_plate_out"] = fmt.Sprintf("%s/images/%s", h.cfg.SelfHost, filenameLic)
	}
	if len(detectImg) > 0 {
		jsonRes["driver_out"] = fmt.Sprintf("%s/images/%s", h.cfg.SelfHost, filenameDet)
	}

	// broadcast
	room := "gate_out_" + c.Query("gate_no")
	h.broadcastJSON(room, jsonRes)

	// LED
	// ไม่แตะ logic LED

	c.String(http.StatusOK, "OK")
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
