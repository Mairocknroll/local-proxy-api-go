package ws

import (
	"log"
	"time"

	"github.com/gorilla/websocket"
)

type Hub struct {
	clients    map[string]map[*websocket.Conn]bool
	broadcast  chan Message
	register   chan Subscription
	unregister chan Subscription
}

type Message struct {
	Group string
	Data  []byte
}

type Subscription struct {
	Group string
	Conn  *websocket.Conn
}

func NewHub() *Hub {
	return &Hub{
		clients:    make(map[string]map[*websocket.Conn]bool),
		broadcast:  make(chan Message),
		register:   make(chan Subscription),
		unregister: make(chan Subscription),
	}
}

func (h *Hub) Run() {
	// กำหนด Timeout สำหรับการส่ง Data (ควรเท่ากับที่เราตั้งใน Handler)
	const writeWait = 10 * time.Second

	for {
		select {
		case s := <-h.register:
			if _, ok := h.clients[s.Group]; !ok {
				h.clients[s.Group] = make(map[*websocket.Conn]bool)
			}
			h.clients[s.Group][s.Conn] = true
			log.Printf("[WS] client joined %s (Total: %d)", s.Group, len(h.clients[s.Group]))

		case s := <-h.unregister:
			if conns, ok := h.clients[s.Group]; ok {
				if _, ok := conns[s.Conn]; ok {
					delete(conns, s.Conn)
					s.Conn.Close()
					log.Printf("[WS] client left %s", s.Group)

					// Optional: ถ้าในห้องไม่มีคนแล้ว ลบห้องทิ้งด้วยก็ได้เพื่อประหยัด Ram
					if len(conns) == 0 {
						delete(h.clients, s.Group)
					}
				}
			}

		case msg := <-h.broadcast:
			if conns, ok := h.clients[msg.Group]; ok {
				for c := range conns {
					// 1. ตั้งเวลาตาย ถ้าส่งไม่ออกภายใน 10 วิ ให้ error เลย
					c.SetWriteDeadline(time.Now().Add(writeWait))

					// 2. ส่งข้อความ
					err := c.WriteMessage(websocket.TextMessage, msg.Data)

					// 3. ถ้าส่งไม่ผ่าน (เน็ตหลุด/Timeout) ให้ลบ Client นั้นทิ้งทันที
					if err != nil {
						log.Printf("[WS] write error: %v, removing client", err)
						c.Close()
						delete(conns, c)

						if len(conns) == 0 {
							delete(h.clients, msg.Group)
						}
					}
				}
			}
		}
	}
}

func (h *Hub) Broadcast(group string, data []byte) {
	h.broadcast <- Message{Group: group, Data: data}
}

func (h *Hub) Register(group string, conn *websocket.Conn) {
	h.register <- Subscription{Group: group, Conn: conn}
}

func (h *Hub) Unregister(group string, conn *websocket.Conn) {
	h.unregister <- Subscription{Group: group, Conn: conn}
}
