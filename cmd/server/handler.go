package main

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"GO_LANG_WORKSPACE/internal/ws"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // ⚠️ production ควรเช็ค origin
	},
}

func serveGateWS(hub *ws.Hub, prefix string) gin.HandlerFunc {
	return func(c *gin.Context) {
		gateNo := c.Param("gate_no")
		group := fmt.Sprintf("%s_%s", prefix, gateNo)

		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			return
		}

		const readWait = 60 * time.Second
		const writeWait = 10 * time.Second // เพิ่ม timeout สำหรับขาส่ง

		conn.SetReadDeadline(time.Now().Add(readWait))

		conn.SetPingHandler(func(appData string) error {
			// 1. ยืดเวลาตาย (Read Deadline)
			conn.SetReadDeadline(time.Now().Add(readWait))

			// 2. ⚠️ เพิ่มบรรทัดนี้: ต้องตอบ Pong กลับไปหา Android ด้วย (ตามกฎ WebSocket)
			err := conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(writeWait))

			// ถ้าตอบกลับไม่ได้ แสดงว่า Connection มีปัญหา ให้ return error เพื่อจบ Loop
			if err == websocket.ErrCloseSent {
				return nil
			} else if e, ok := err.(net.Error); ok && e.Temporary() {
				return nil
			}
			return err
		})

		hub.Register(group, conn)
		defer hub.Unregister(group, conn)

		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	}
}

func serveZoningWS(hub *ws.Hub, prefix string) gin.HandlerFunc {
	return func(c *gin.Context) {
		zoningCode := c.Param("zoning_code")
		gateNo := c.Param("gate_no")
		group := fmt.Sprintf("%s:%s:%s", prefix, zoningCode, gateNo)

		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			return
		}

		const readWait = 60 * time.Second
		const writeWait = 10 * time.Second // เพิ่ม timeout สำหรับขาส่ง

		conn.SetReadDeadline(time.Now().Add(readWait))

		conn.SetPingHandler(func(appData string) error {
			// 1. ยืดเวลาตาย (Read Deadline)
			conn.SetReadDeadline(time.Now().Add(readWait))

			// 2. ⚠️ เพิ่มบรรทัดนี้: ต้องตอบ Pong กลับไปหา Android ด้วย (ตามกฎ WebSocket)
			err := conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(writeWait))

			// ถ้าตอบกลับไม่ได้ แสดงว่า Connection มีปัญหา ให้ return error เพื่อจบ Loop
			if err == websocket.ErrCloseSent {
				return nil
			} else if e, ok := err.(net.Error); ok && e.Temporary() {
				return nil
			}
			return err
		})

		hub.Register(group, conn)
		defer hub.Unregister(group, conn)

		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break // ถ้า Error หรือ Connection หลุด ให้ break ออกจาก Loop เพื่อทำลาย connection
			}
		}
	}
}
