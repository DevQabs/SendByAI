// Package hub manages WebSocket client connections and the message moderation pipeline.
package hub

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"

	"github.com/sendbyai/sendbyai/internal/filter"
)

// Hub is the central broker: it registers/unregisters clients and drives
// every message through the filter chain before broadcasting.
type Hub struct {
	mu    sync.RWMutex
	rooms map[string]map[string]*Client // roomID → clientID → *Client

	inbound    chan *InboundMessage
	register   chan *Client
	unregister chan *Client

	chain *filter.Chain
}

// New creates a Hub with the provided filter chain.
func New(chain *filter.Chain) *Hub {
	return &Hub{
		rooms:      make(map[string]map[string]*Client),
		inbound:    make(chan *InboundMessage, 512),
		register:   make(chan *Client, 64),
		unregister: make(chan *Client, 64),
		chain:      chain,
	}
}

// Run is the hub event loop. Call it in a dedicated goroutine.
// It exits when ctx is cancelled.
func (h *Hub) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case c := <-h.register:
			h.addClient(c)
		case c := <-h.unregister:
			h.removeClient(c)
		case msg := <-h.inbound:
			// Each message processed in its own goroutine so slow AI filters
			// (Step 2 / Step 3) never stall the event loop.
			go h.process(ctx, msg)
		}
	}
}

// ServeWS upgrades an HTTP connection to WebSocket and registers the client.
// Query params: user_id, room_id (both required).
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	roomID := r.URL.Query().Get("room_id")
	if userID == "" || roomID == "" {
		http.Error(w, "user_id and room_id are required", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("ws upgrade failed", "err", err)
		return
	}

	c := newClient(h, conn, userID, roomID)
	h.register <- c

	go c.writePump()
	go c.readPump()
}

// process runs a single message through the filter chain then broadcasts.
// Runs in its own goroutine — safe to block on slow AI inference.
func (h *Hub) process(ctx context.Context, msg *InboundMessage) {
	fMsg := &filter.Message{
		RoomID:  msg.RoomID,
		UserID:  msg.UserID,
		Content: msg.Content,
	}

	result, err := h.chain.Filter(ctx, fMsg)
	if err != nil {
		slog.Error("filter pipeline error", "user", msg.UserID, "err", err)
		return
	}

	switch result.Action {
	case filter.ActionBlock:
		slog.Info("message blocked",
			"user", msg.UserID, "room", msg.RoomID,
			"reason", result.Reason, "score", result.Score)
		return
	case filter.ActionQuarantine:
		slog.Info("message quarantined",
			"user", msg.UserID, "room", msg.RoomID,
			"score", result.Score)
		// TODO Step 2/3: forward to quarantine store / admin dashboard
		return
	}

	out := &OutboundMessage{
		UserID:  result.Message.UserID,
		Content: result.Message.Content,
		At:      msg.At,
	}
	h.broadcast(msg.RoomID, out)
}

func (h *Hub) broadcast(roomID string, out *OutboundMessage) {
	raw, err := json.Marshal(out)
	if err != nil {
		slog.Error("marshal outbound message", "err", err)
		return
	}

	h.mu.RLock()
	clients := h.rooms[roomID]
	h.mu.RUnlock()

	for _, c := range clients {
		select {
		case c.send <- raw:
		default:
			// Slow consumer — drop to avoid blocking the broadcast loop.
			slog.Warn("slow client, message dropped", "client", c.id)
		}
	}
}

func (h *Hub) addClient(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.rooms[c.roomID] == nil {
		h.rooms[c.roomID] = make(map[string]*Client)
	}
	h.rooms[c.roomID][c.id] = c
	slog.Info("client joined", "client", c.id, "user", c.userID, "room", c.roomID)
}

func (h *Hub) removeClient(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	room, ok := h.rooms[c.roomID]
	if !ok {
		return
	}
	if _, ok := room[c.id]; ok {
		delete(room, c.id)
		close(c.send)
	}
	if len(room) == 0 {
		delete(h.rooms, c.roomID)
	}
	slog.Info("client left", "client", c.id, "user", c.userID, "room", c.roomID)
}
