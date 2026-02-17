package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	mqttsvc "GO_LANG_WORKSPACE/cmd/server/mqtt"
	"GO_LANG_WORKSPACE/internal/barrier_v2"
	"GO_LANG_WORKSPACE/internal/config"
	"GO_LANG_WORKSPACE/internal/image_v2"
	"GO_LANG_WORKSPACE/internal/order"
	"GO_LANG_WORKSPACE/internal/reserve"
	"GO_LANG_WORKSPACE/internal/ws"
	zoningpkg "GO_LANG_WORKSPACE/internal/zoning"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"

	// Swagger
	_ "GO_LANG_WORKSPACE/cmd/server/docs"
	docs "GO_LANG_WORKSPACE/cmd/server/docs"

	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

// Healthz godoc
// @Summary      Health check
// @Description  Return OK if server alive
// @Tags         health
// @Produce      json
// @Success      200 {object} map[string]string
// @Router       /healthz [get]
func Healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// @title           Local Proxy API
// @version         1.0
// @description     Gateway สำหรับกล้อง/อุปกรณ์ → proxy ไปยัง services ภายใน
// @contact.name    Your Name
// @contact.email   you@example.com
// @BasePath        /
// @schemes         http
func main() {
	// ---------- .env ----------
	// ลองโหลด .env จากหลายที่เพื่อรองรับทั้ง direct run และ air
	cwd, _ := os.Getwd()
	envPaths := []string{
		".env",                           // current directory
		filepath.Join(cwd, ".env"),       // working directory
		"../../.env",                     // สำหรับกรณี run จาก cmd/server
		filepath.Join(cwd, "../../.env"), // absolute path to parent
	}

	envLoaded := false
	for _, path := range envPaths {
		if err := godotenv.Load(path); err == nil {
			log.Printf("[env] loaded from %s", path)
			envLoaded = true
			break
		}
	}
	if !envLoaded {
		log.Println("[env] no .env file found, using system environment variables")
	}

	// ---------- Context (SIGINT/SIGTERM) ----------
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// ---------- Config ----------
	cfg := config.Load()

	// ---------- MQTT listener ----------
	listener := mqttsvc.NewFromEnv()
	go func() {
		log.Printf("[startup] starting MQTT listener (host auto-picked by SERVER_URL)")
		if err := listener.Start(ctx); err != nil {
			log.Printf("[fatal] mqtt listener error: %v", err)
		}
	}()

	// ---------- WebSocket hub ----------
	hub := ws.NewHub()
	go hub.Run()

	// ---------- Gin ----------
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery(), config.RequestIDMiddleware(), config.LoggerMiddleware())

	// ---------- Swagger ----------
	docs.SwaggerInfo.BasePath = "/"
	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	// ---------- Health ----------
	r.GET("/healthz", Healthz)

	// ---------- WebSocket rooms ----------
	r.GET("/gate-in/:gate_no", serveGateWS(hub, "gate_in"))
	r.GET("/reserve-in/:gate_no", serveGateWS(hub, "reserve_in"))
	r.GET("/reserve-out/:gate_no", serveGateWS(hub, "reserve_out"))
	r.GET("/gate-out/:gate_no", serveGateWS(hub, "gate_out"))
	r.GET("/zoning/entrance/:zoning_code/:gate_no", serveZoningWS(hub, "entrance"))
	r.GET("/zoning/exit/:zoning_code/:gate_no", serveZoningWS(hub, "exit"))

	// ---------- API group ----------
	api := r.Group("/api")
	{
		v1 := api.Group("/v2-202402")
		{
			// Order
			Order := order.NewHandler(cfg, hub)
			orderGroup := v1.Group("/order")
			{
				orderGroup.POST("/verify-member", Order.VerifyMember)
				orderGroup.POST("/verify-license-plate-out", Order.VerifyLicensePlateOut)
			}

			// Reserve
			Reserve := reserve.NewHandler(cfg, hub)
			reserveGroup := v1.Group("/reserve")
			{
				reserveGroup.POST("/entrance", Reserve.VerifyReserve)
				reserveGroup.POST("/exit", Reserve.VerifyReserveExit)
			}

			// Barrier
			gateGroup := v1.Group("/gate")
			{
				gateGroup.GET("/open-barrier/:direction/:gate", barrier_v2.OpenBarrier)
				gateGroup.GET("/close-barrier/:direction/:gate", barrier_v2.CloseBarrier)
				gateGroup.GET("/open-zoning/:direction/:gate", barrier_v2.OpenZoning)
				gateGroup.GET("/close-zoning/:direction/:gate", barrier_v2.CloseZoning)
			}

			// Zoning
			zn := zoningpkg.NewHandler(cfg, hub)
			routeZoning := v1.Group("/zoning")
			{
				routeZoning.POST("/entrance/:zoning_code", zn.ZoningEntrance)
				routeZoning.POST("/exit/:zoning_code", zn.ZoningExit)
				routeZoning.GET("/exit/:zoning_code", func(c *gin.Context) {
					c.JSON(http.StatusOK, gin.H{"ok": true})
				})
			}
		}

		// Image v2 (อยู่นอก v2-202402 ตามของเดิม)
		v2img := api.Group("/v2-202401/image")
		{
			v2img.POST("/collect-image/:gate_no", image_v2.CollectImage(cfg))
			v2img.POST("/collect-image-none/:gate_no", image_v2.CollectImageNone(cfg))
			v2img.GET("/get-license-plate-picture", image_v2.GetLicensePlatePicture(cfg))
			v2img.POST("/checkout-vehicle/:gate_no", image_v2.CheckoutVehicle(cfg))
		}
	}

	// ---------- แสดงทุก route เพื่อเช็ค ----------
	for _, rt := range r.Routes() {
		log.Printf("[route] %s %s", rt.Method, rt.Path)
	}

	// ---------- HTTP server (timeouts + graceful shutdown) ----------
	srv := &http.Server{
		Addr:              ":8000",
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second, // เวลาอ่าน body รวม (ช่วย multipart upload)
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1MB header
	}

	// เริ่ม server ใน goroutine เพื่อให้ปิดแบบ graceful ได้
	go func() {
		log.Printf("[startup] listening on %s (SERVER_URL=%s, PARKING_CODE=%s)", srv.Addr, cfg.ServerURL, cfg.ParkingCode)
		log.Printf("[startup] Swagger → http://localhost:8000/swagger/index.html")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	// รอสัญญาณปิด
	<-ctx.Done()
	log.Println("[shutdown] signal received")

	// ปิดด้วย timeout เผื่อให้ request ค้าง ๆ จบก่อน
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("[shutdown] graceful shutdown failed: %v", err)
	} else {
		log.Println("[shutdown] graceful shutdown complete")
	}
}
