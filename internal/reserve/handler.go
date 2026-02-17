package reserve

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
	"strings"
	"time"

	digest "github.com/icholy/digest"

	"GO_LANG_WORKSPACE/internal/barrier_v2"
	"GO_LANG_WORKSPACE/internal/config"
	"GO_LANG_WORKSPACE/internal/utils"
	"GO_LANG_WORKSPACE/internal/ws"

	"github.com/gin-gonic/gin"
)

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

type Handler struct {
	cfg        *config.Config
	hub        *ws.Hub
	httpClient *http.Client
	camClient  *http.Client
}

func NewHandler(cfg *config.Config, hub *ws.Hub) *Handler {
	httpCli := &http.Client{
		Timeout:   6 * time.Second,
		Transport: config.NewHTTPTransport(),
	}

	camTr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
	}
	camDT := &digest.Transport{
		Username:  cfg.CameraUser,
		Password:  cfg.CameraPass,
		Transport: camTr,
	}
	camCli := &http.Client{
		Timeout:   5 * time.Second,
		Transport: camDT,
	}

	return &Handler{
		cfg:        cfg,
		hub:        hub,
		httpClient: httpCli,
		camClient:  camCli,
	}
}

// VerifyReserve = POST /api/v2-202402/reserve/verify-reserve
func (h *Handler) VerifyReserve(c *gin.Context) {
	t0 := time.Now()

	// ---------- Step 1: Validate Request + limit body ----------
	ct := c.GetHeader("Content-Type")
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		c.String(http.StatusOK, "Invalid request")
		return
	}

	const maxBody = int64(16 << 20) // 16MB
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBody)
	defer c.Request.Body.Close()

	mr := multipart.NewReader(c.Request.Body, params["boundary"])
	t1 := time.Since(t0)

	// ---------- Step 2: Separate Files ----------
	var xmlBuf []byte
	var lpImg []byte
	var detectImg = []byte(nil)

	parts := 0
	const maxParts = 10

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				c.String(http.StatusRequestTimeout, "multipart read timeout")
			} else {
				c.String(http.StatusOK, "invalid multipart")
			}
			return
		}

		parts++
		if parts > maxParts {
			_ = part.Close()
			c.String(http.StatusOK, "too many parts")
			return
		}

		fn := part.FileName()
		if fn == "" {
			_, _ = io.Copy(io.Discard, part)
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
			detectImg = buf
		default:
		}
	}

	if len(xmlBuf) == 0 {
		c.String(http.StatusOK, "Missing XML file")
		return
	}
	t2 := time.Since(t0) - t1

	// ---------- Step 3: Parse XML ----------
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
				vehicleType = strings.TrimSpace(ev.ANPR.VehicleType)
			}
		}
	}
	if plate == "" {
		c.String(http.StatusOK, "Failed to parse XML")
		return
	}
	t3 := time.Since(t0) - t1 - t2

	// ---------- Step 4: Call Reserve API instead of Get Customer ID ----------
	gateNo := c.Query("gate_no")
	t4 := time.Since(t0) - t1 - t2 - t3

	go h.postParkingLicensePlate(plate, ip, h.cfg.ParkingCode)
	t5 := time.Since(t0) - t1 - t2 - t3 - t4

	// NEW LOGIC START
	apiURL := fmt.Sprintf("%s/api/v1/reserve/entrance-lpr", h.cfg.ServerURL)
	body := map[string]any{
		"LicensePlate": plate,
		"ParkingCode":  h.cfg.ParkingCode,
	}

	// Log always
	// Check status
	var jsonRes map[string]any
	isSuccess := false

	b, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.httpClient.Do(req)

	if err != nil {
		log.Printf("[VerifyReserve] API Request Error: %v", err)
	} else {
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[VerifyReserve] API Response Status: %d, Body: %s", resp.StatusCode, string(respBody))

		if resp.StatusCode == 200 {
			isSuccess = true
			// Try to parse json if needed for broadcast?
			_ = json.Unmarshal(respBody, &jsonRes)
		}
	}

	t6 := time.Since(t0) - t1 - t2 - t3 - t4 - t5

	// 3. If 200 -> open barrier
	if isSuccess {
		if err := barrier_v2.OpenReserveBarrierByGate("ENT", gateNo); err != nil {
			log.Printf("[VerifyReserve] Open Barrier Error: %v", err)
		} else {
			log.Printf("[VerifyReserve] Barrier Opened for gate %s", gateNo)
		}
	}

	if lpImg == nil {
		lpImg = detectImg
	}

	payload := map[string]any{
		"license_plate":            plate,
		"uuid":                     uuid,
		"time_in":                  timeIn,
		"vehicle_type":             utils.VehicleType(vehicleType),
		"license_plate_img_base64": base64.StdEncoding.EncodeToString(lpImg),
		"api_response":             jsonRes, // Add api response to broadcast as requested
	}

	// 4. Broadcast to /reserve-in/:gate_no
	room := "reserve_in_" + gateNo
	h.broadcastJSON(room, payload)

	t7 := time.Since(t0) - t1 - t2 - t3 - t4 - t5 - t6

	h.logTimingsEntrance(c, t0, t1, t2, t3, t4, t5, t6, t7, plate)
	c.String(http.StatusOK, "File(s) uploaded successfully")
}

// VerifyReserveExit = POST /api/v2-202402/reserve/verify-reserve-exit?gate_no=1
func (h *Handler) VerifyReserveExit(c *gin.Context) {
	t0 := time.Now()

	// ---------- Step 1: Validate Request + limit body ----------
	ct := c.GetHeader("Content-Type")
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		c.String(http.StatusOK, "Invalid request")
		return
	}

	const maxBody = int64(16 << 20) // 16MB
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBody)
	defer c.Request.Body.Close()

	mr := multipart.NewReader(c.Request.Body, params["boundary"])
	t1 := time.Since(t0)

	// ---------- Step 2: Separate Files ----------
	var xmlBuf []byte
	var lpImg []byte
	var detectImg = []byte(nil)

	parts := 0
	const maxParts = 10

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				c.String(http.StatusRequestTimeout, "multipart read timeout")
			} else {
				c.String(http.StatusOK, "invalid multipart")
			}
			return
		}

		parts++
		if parts > maxParts {
			_ = part.Close()
			c.String(http.StatusOK, "too many parts")
			return
		}

		fn := part.FileName()
		if fn == "" {
			_, _ = io.Copy(io.Discard, part)
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
			detectImg = buf
		default:
		}
	}

	if len(xmlBuf) == 0 {
		c.String(http.StatusOK, "Missing XML file")
		return
	}
	t2 := time.Since(t0) - t1

	// ---------- Step 3: Parse XML ----------
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
				vehicleType = strings.TrimSpace(ev.ANPR.VehicleType)
			}
		}
	}
	if plate == "" {
		c.String(http.StatusOK, "Failed to parse XML")
		return
	}
	t3 := time.Since(t0) - t1 - t2

	// ---------- Step 4: Call Reserve Exit API ----------
	gateNo := c.Query("gate_no")
	t4 := time.Since(t0) - t1 - t2 - t3

	go h.postParkingLicensePlate(plate, ip, h.cfg.ParkingCode)
	t5 := time.Since(t0) - t1 - t2 - t3 - t4

	// NEW LOGIC START (Exit)
	apiURL := fmt.Sprintf("%s/api/v1/reserve/exit-lpr", h.cfg.ServerURL)
	body := map[string]any{
		"LicensePlate": plate,
		"ParkingCode":  h.cfg.ParkingCode,
	}

	var jsonRes map[string]any
	isSuccess := false

	b, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.httpClient.Do(req)

	if err != nil {
		log.Printf("[VerifyReserveExit] API Request Error: %v", err)
	} else {
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[VerifyReserveExit] API Response Status: %d, Body: %s", resp.StatusCode, string(respBody))

		if resp.StatusCode == 200 {
			isSuccess = true
			_ = json.Unmarshal(respBody, &jsonRes)
		}
	}

	t6 := time.Since(t0) - t1 - t2 - t3 - t4 - t5

	// 3. If 200 -> open barrier (EXT)
	if isSuccess {
		if err := barrier_v2.OpenReserveBarrierByGate("EXT", gateNo); err != nil {
			log.Printf("[VerifyReserveExit] Open Barrier Error: %v", err)
		} else {
			log.Printf("[VerifyReserveExit] Barrier Opened for gate %s", gateNo)
		}
	}

	if lpImg == nil {
		lpImg = detectImg
	}

	payload := map[string]any{
		"license_plate":            plate,
		"uuid":                     uuid,
		"time_in":                  timeIn,
		"vehicle_type":             utils.VehicleType(vehicleType),
		"license_plate_img_base64": base64.StdEncoding.EncodeToString(lpImg),
		"api_response":             jsonRes,
	}

	// 4. Broadcast to /reserve-out/:gate_no
	room := "reserve_out_" + gateNo
	h.broadcastJSON(room, payload)

	t7 := time.Since(t0) - t1 - t2 - t3 - t4 - t5 - t6

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

func (h *Handler) broadcastJSON(room string, payload map[string]any) {
	b, _ := json.Marshal(payload)
	h.hub.Broadcast(room, b)
}

func (h *Handler) logTimingsEntrance(c *gin.Context, t0 time.Time, t1, t2, t3, t4, t5, t6, t7 time.Duration, plate string) {
	log.Printf("[RESERVE IN %s: LICENSE PLATE: %s]", c.Query("gate_no"), plate)
	log.Printf(" - Parse Multipart:  %.2fs", t1.Seconds())
	log.Printf(" - Split Files:      %.2fs", t2.Seconds())
	log.Printf(" - Parse XML:        %.2fs", t3.Seconds())
	log.Printf(" - De-dup:           %.2fs", t4.Seconds())
	log.Printf(" - plpPost:          %.2fs", t5.Seconds())
	log.Printf(" - Call API:         %.2fs", t6.Seconds())
	log.Printf(" - Broadcast:        %.2fs", t7.Seconds())
	log.Printf(" - Total Time:       %.2fs", time.Since(t0).Seconds())
}

func (h *Handler) logTimingsExit(c *gin.Context, t0 time.Time, t1, t2, t3, t4, t5, t6, t7 time.Duration, plate string) {
	log.Printf("[RESERVE OUT %s: LICENSE PLATE: %s]", c.Query("gate_no"), plate)
	log.Printf(" - Parse Multipart:  %.2fs", t1.Seconds())
	log.Printf(" - Split Files:      %.2fs", t2.Seconds())
	log.Printf(" - Parse XML:        %.2fs", t3.Seconds())
	log.Printf(" - De-dup:           %.2fs", t4.Seconds())
	log.Printf(" - plpPost:          %.2fs", t5.Seconds())
	log.Printf(" - Call API:         %.2fs", t6.Seconds())
	log.Printf(" - Broadcast:        %.2fs", t7.Seconds())
	log.Printf(" - Total Time:       %.2fs", time.Since(t0).Seconds())
}
