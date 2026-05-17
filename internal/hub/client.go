package hub

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 4096
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// Tighten CheckOrigin in production — validate r.Header.Get("Origin").
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Client represents a single WebSocket connection.
type Client struct {
	id     string
	userID string
	roomID string
	hub    *Hub
	conn   *websocket.Conn
	send   chan []byte // buffered outbound messages
}

func newClient(h *Hub, conn *websocket.Conn, userID, roomID string) *Client {
	return &Client{
		id:     uuid.New().String(),
		userID: userID,
		roomID: roomID,
		hub:    h,
		conn:   conn,
		send:   make(chan []byte, 256),
	}
}

// readPump pumps inbound messages from the WebSocket to the hub.
// One goroutine per client; runs until the connection closes.
func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				slog.Error("ws read error", "client", c.id, "err", err)
			}
			break
		}
		var content string
		var in struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(raw, &in); err == nil && in.Content != "" {
			content = in.Content
		} else {
			content = strings.TrimSpace(string(raw))
		}
		if content == "" {
			continue
		}
		c.hub.inbound <- &InboundMessage{
			ClientID: c.id,
			RoomID:   c.roomID,
			UserID:   c.userID,
			Content:  content,
			At:       time.Now(),
		}
	}
}

// writePump pumps outbound messages from the hub to the WebSocket.
// One goroutine per client; also handles keep-alive pings.
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
