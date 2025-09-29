package main

import (
	"log"

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

	// WebSocket: ws://<host>/ws?group=gate_in_1

	// API group
	order := order.NewHandler(cfg, hub)
	api := r.Group("/api/v2-202402/order")

	{
		api.POST("/verify-member", order.VerifyMember)
		api.POST("/verify-license-plate-out", order.VerifyLicensePlateOut)
		r.GET("/gate-in/:gate_no", serveGateWS(hub, "gate_in"))
		r.GET("/gate-out/:gate_no", serveGateWS(hub, "gate_out"))
	}

	// Run server
	srv := config.NewHTTPServer(cfg, r)
	log.Printf("[startup] listening on %s (SERVER_URL=%s, PARKING_CODE=%s)", srv.Addr, cfg.ServerURL, cfg.ParkingCode)

	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
