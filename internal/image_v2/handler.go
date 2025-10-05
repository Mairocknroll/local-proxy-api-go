package image_v2

import (
	"GO_LANG_WORKSPACE/internal/config"
	"GO_LANG_WORKSPACE/internal/utils"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"
)

// ENV
var (
	serverURL   = getenv("SERVER_URL", "")
	parkingCode = getenv("PARKING_CODE", "")
)

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// สคีมาเหมือนเดิม
type UploadImage struct {
	UUID                  string  `json:"uuid"`
	LicensePlate          string  `json:"license_plate"`
	ParkCode              string  `json:"park_code"`
	TimeStamp             string  `json:"time_stamp"`
	Gate                  string  `json:"gate"`
	LicensePlateImgBase64 string  `json:"license_plate_img_base64"`
	DriverImgBase64       *string `json:"driver_img_base_64"`
}

func CollectImage(cfg *config.Config) gin.HandlerFunc {
	reGate := regexp.MustCompile(`^[0-9]+$`)
	return func(c *gin.Context) {
		gateNo := c.Param("gate_no")
		if !reGate.MatchString(gateNo) {
			c.JSON(http.StatusBadRequest, gin.H{"status": false, "message": "invalid gate number"})
			return
		}

		var in UploadImage
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": false, "message": err.Error()})
			return
		}
		in.Gate = "ent"
		if in.ParkCode == "" {
			in.ParkCode = parkingCode
		}

		// ดึงเฉพาะรูป "Driver"
		driverB64, err := utils.FetchDriverImage(cfg, gateNo)
		if err != nil {
			empty := ""
			in.DriverImgBase64 = &empty
		} else {
			in.DriverImgBase64 = &driverB64
		}

		if serverURL == "" {
			c.JSON(http.StatusInternalServerError, gin.H{"status": false, "message": "SERVER_URL not configured"})
			return
		}
		url := fmt.Sprintf("%s/api/v1-202401/image/collect-image", serverURL)

		body, _ := json.Marshal(in)
		req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		httpClient := &http.Client{Timeout: 10 * time.Second}
		resp, err := httpClient.Do(req)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"status": false, "message": err.Error()})
			return
		}
		defer resp.Body.Close()

		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"status": false, "message": "invalid upstream response"})
			return
		}
		c.JSON(resp.StatusCode, out)
	}
}

func GetLicensePlatePicture(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		gateNo := c.Query("gate_no")
		if gateNo == "" {
			c.JSON(http.StatusBadRequest, gin.H{"status": false, "message": "gate_no is required"})
			return
		}

		// ดึงเฉพาะรูป "License Plate"
		lpB64, err := utils.FetchLicensePlateImage(cfg, gateNo)
		if err != nil || lpB64 == "" {
			c.JSON(http.StatusOK, gin.H{
				"status":  false,
				"message": "Failed to fetch license plate snapshot",
				"data":    nil,
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"status":  true,
			"message": "Success",
			"data":    lpB64,
		})
	}
}
