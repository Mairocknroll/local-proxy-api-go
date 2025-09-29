package main

import (
	"fmt"
	"net/http"

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
		hub.Register(group, conn)
		defer hub.Unregister(group, conn)

		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				break
			}
			hub.Broadcast(group, msg)
		}
	}
}
