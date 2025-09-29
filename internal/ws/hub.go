package ws

import (
	"log"

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
	for {
		select {
		case s := <-h.register:
			if _, ok := h.clients[s.Group]; !ok {
				h.clients[s.Group] = make(map[*websocket.Conn]bool)
			}
			h.clients[s.Group][s.Conn] = true
			log.Printf("[WS] client joined %s", s.Group)

		case s := <-h.unregister:
			if conns, ok := h.clients[s.Group]; ok {
				if _, ok := conns[s.Conn]; ok {
					delete(conns, s.Conn)
					s.Conn.Close()
					log.Printf("[WS] client left %s", s.Group)
				}
			}

		case msg := <-h.broadcast:
			if conns, ok := h.clients[msg.Group]; ok {
				for c := range conns {
					c.WriteMessage(websocket.TextMessage, msg.Data)
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
