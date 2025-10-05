package main

import (
	"GO_LANG_WORKSPACE/internal/image_v2"
	"log"

	"GO_LANG_WORKSPACE/internal/barrier_v2"
	"GO_LANG_WORKSPACE/internal/config"
	"GO_LANG_WORKSPACE/internal/order"
	"GO_LANG_WORKSPACE/internal/ws"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file found")
	}

	cfg := config.Load()

	// WS Hub
	hub := ws.NewHub()
	go hub.Run()

	// Gin
	r := gin.New()
	gin.SetMode(gin.ReleaseMode)
	r.Use(gin.Recovery(), config.RequestIDMiddleware(), config.LoggerMiddleware())

	// Health check
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// WebSocket rooms (ตามของเดิม)
	r.GET("/gate-in/:gate_no", serveGateWS(hub, "gate_in"))
	r.GET("/gate-out/:gate_no", serveGateWS(hub, "gate_out"))

	// API group
	api := r.Group("/api")
	{
		v1 := api.Group("/v2-202402")
		{
			// Order handlers
			Order := order.NewHandler(cfg, hub)
			orderGroup := v1.Group("/order")
			{
				orderGroup.POST("/verify-member", Order.VerifyMember)
				orderGroup.POST("/verify-license-plate-out", Order.VerifyLicensePlateOut)
			}

			// Barrier handlers (ผูกตรง ๆ ไม่ต้อง new)
			gateGroup := v1.Group("/gate")
			{
				gateGroup.GET("/open-barrier/:direction/:gate", barrier_v2.OpenBarrier)
				gateGroup.GET("/close-barrier/:direction/:gate", barrier_v2.CloseBarrier)

				gateGroup.GET("/open-zoning/:direction/:gate", barrier_v2.OpenZoning)
				gateGroup.GET("/close-zoning/:direction/:gate", barrier_v2.CloseZoning)
			}

			v2img := api.Group("/v2-202401/image") // ให้ตรง prefix เดิมของ Python
			{
				// POST /api/v2-202401/image/collect-image/:gate_no
				v2img.POST("/collect-image/:gate_no", image_v2.CollectImage(cfg))

				// GET /api/v2-202401/image/get-license-plate-picture?gate_no=1
				v2img.GET("/get-license-plate-picture", image_v2.GetLicensePlatePicture(cfg))
			}
		}
	}

	// Run server
	srv := config.NewHTTPServer(cfg, r)
	log.Printf("[startup] listening on %s (SERVER_URL=%s, PARKING_CODE=%s)", srv.Addr, cfg.ServerURL, cfg.ParkingCode)

	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
