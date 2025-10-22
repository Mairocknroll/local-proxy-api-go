package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	mqttsvc "GO_LANG_WORKSPACE/cmd/server/mqtt"
	"GO_LANG_WORKSPACE/internal/barrier_v2"
	"GO_LANG_WORKSPACE/internal/config"
	"GO_LANG_WORKSPACE/internal/image_v2"
	"GO_LANG_WORKSPACE/internal/order"
	"GO_LANG_WORKSPACE/internal/ws"
	zoningpkg "GO_LANG_WORKSPACE/internal/zoning"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file found")
	}

	// ctx สำหรับ graceful shutdown ทั้งแอป
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := config.Load()

	// ---------- Start MQTT listener (background) ----------
	listener := mqttsvc.NewFromEnv()
	go func() {
		log.Printf("[startup] starting MQTT listener (host auto-picked by SERVER_URL)")
		if err := listener.Start(ctx); err != nil {
			log.Printf("[fatal] mqtt listener error: %v", err)
			// ถ้าอยากให้แอปปิดเมื่อ listener error ครั้งแรก: stop()
		}
	}()
	// -----------------------------------------------------

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

	// WebSocket rooms
	r.GET("/gate-in/:gate_no", serveGateWS(hub, "gate_in"))
	r.GET("/gate-out/:gate_no", serveGateWS(hub, "gate_out"))

	r.GET("/zoning/entrance/:zoning_code/:gate_no", serveZoningWS(hub, "entrance"))
	r.GET("/zoning/exit/:zoning_code/:gate_no", serveZoningWS(hub, "exit"))

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

			// Barrier handlers
			gateGroup := v1.Group("/gate")
			{
				gateGroup.GET("/open-barrier/:direction/:gate", barrier_v2.OpenBarrier)
				gateGroup.GET("/close-barrier/:direction/:gate", barrier_v2.CloseBarrier)

				gateGroup.GET("/open-zoning/:direction/:gate", barrier_v2.OpenZoning)
				gateGroup.GET("/close-zoning/:direction/:gate", barrier_v2.CloseZoning)
			}

			// Image v2
			v2img := api.Group("/v2-202401/image")
			{
				v2img.POST("/collect-image/:gate_no", image_v2.CollectImage(cfg))
				v2img.GET("/get-license-plate-picture", image_v2.GetLicensePlatePicture(cfg))
			}

			// Zoning handlers
			zn := zoningpkg.NewHandler(cfg, hub)
			routeZoning := v1.Group("/zoning")
			{
				routeZoning.POST("/entrance/:zoning_code", zn.ZoningEntrance)
				routeZoning.POST("/exit/:zoning_code", zn.ZoningExit)
				routeZoning.GET("/exit/:zoning_code", func(c *gin.Context) {
					c.JSON(200, gin.H{"ok": true})
				})
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
