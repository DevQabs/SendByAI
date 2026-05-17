// Package hub manages WebSocket client connections and the message moderation pipeline.
package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"

	"github.com/sendbyai/sendbyai/internal/filter"
)

// Hub is the central broker: it registers/unregisters clients and drives
// every message through the filter pipeline before broadcasting.
//
// Two-stage pipeline:
//  1. fastFilter (e.g. ONNX classifier) — blocks synchronously before send
//  2. deepFilter (e.g. Ollama LLM) — runs async after optimistic broadcast
//     for messages that the fast filter quarantined
type Hub struct {
	mu    sync.RWMutex
	rooms map[string]map[string]*Client // roomID → clientID → *Client

	inbound    chan *InboundMessage
	register   chan *Client
	unregister chan *Client

	fastFilter filter.Filter // runs before broadcast; block/allow decisions are final
	deepFilter filter.Filter // re-judges quarantined messages after optimistic broadcast
}

// New creates a Hub.
// fast: synchronous pre-broadcast filter (e.g. unsmile ONNX)
// deep: async post-broadcast filter for quarantined messages (e.g. Ollama)
func New(fast, deep filter.Filter) *Hub {
	return &Hub{
		rooms:      make(map[string]map[string]*Client),
		inbound:    make(chan *InboundMessage, 512),
		register:   make(chan *Client, 64),
		unregister: make(chan *Client, 64),
		fastFilter: fast,
		deepFilter: deep,
	}
}

// Run is the hub event loop. Call it in a dedicated goroutine.
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

// process runs the two-stage moderation pipeline.
//
// Stage 1 (fast, synchronous):
//   - Allow  → broadcast immediately
//   - Block  → drop silently
//   - Quarantine → broadcast optimistically with a msg_id, then stage 2
//
// Stage 2 (deep, async — only for quarantined messages):
//   - Allow      → no-op (already visible)
//   - Quarantine → broadcast warn{msg_id}
//   - Block      → broadcast delete{msg_id}
func (h *Hub) process(ctx context.Context, msg *InboundMessage) {
	fMsg := &filter.Message{
		RoomID:  msg.RoomID,
		UserID:  msg.UserID,
		Content: msg.Content,
	}

	result, err := h.fastFilter.Filter(ctx, fMsg)
	if err != nil {
		slog.Error("fast filter error", "user", msg.UserID, "err", err)
		return
	}

	switch result.Action {
	case filter.ActionBlock:
		slog.Info("message blocked (fast)",
			"user", msg.UserID, "room", msg.RoomID,
			"reason", result.Reason, "score", result.Score)
		h.sendToClient(msg.ClientID, &OutboundMessage{
			Type:   "block",
			Reason: result.Reason,
			Score:  result.Score,
		})
		return

	case filter.ActionAllow, filter.ActionReplace:
		out := &OutboundMessage{
			Type:    "message",
			MsgID:   newMsgID(),
			UserID:  result.Message.UserID,
			Content: result.Message.Content,
			At:      timePtr(msg.At),
		}
		h.broadcast(msg.RoomID, out)

	case filter.ActionQuarantine:
		msgID := newMsgID()
		slog.Info("message quarantined (fast), broadcasting optimistically",
			"user", msg.UserID, "room", msg.RoomID,
			"msg_id", msgID, "score", result.Score, "reason", result.Reason)

		// Stage 1 quarantine: propagate signal for deep filter
		if fMsg.Meta == nil {
			fMsg.Meta = make(map[string]any)
		}
		fMsg.Meta["quarantined"] = true
		fMsg.Meta["quarantine_reason"] = result.Reason
		fMsg.Meta["quarantine_score"] = result.Score

		h.broadcast(msg.RoomID, &OutboundMessage{
			Type:    "message",
			MsgID:   msgID,
			UserID:  msg.UserID,
			Content: msg.Content,
			At:      timePtr(msg.At),
		})
		// Immediately warn clients — unsmile already flagged this as suspicious.
		// Ollama will clear or escalate asynchronously.
		h.broadcast(msg.RoomID, &OutboundMessage{
			Type:   "warn",
			MsgID:  msgID,
			UserID: msg.UserID,
			Reason: result.Reason,
			Score:  result.Score,
		})

		// Stage 2: async deep judgment
		go h.deepJudge(ctx, msg.RoomID, msg.UserID, msgID, fMsg)
	}
}

// deepJudge calls the deep filter on a previously quarantined message and
// broadcasts a warn or delete event based on the verdict.
func (h *Hub) deepJudge(ctx context.Context, roomID, userID, msgID string, fMsg *filter.Message) {
	result, err := h.deepFilter.Filter(ctx, fMsg)
	if err != nil {
		slog.Warn("deep filter error, treating as quarantine",
			"user", fMsg.UserID, "msg_id", msgID, "err", err)
		h.broadcast(roomID, &OutboundMessage{Type: "warn", MsgID: msgID, UserID: userID})
		return
	}

	switch result.Action {
	case filter.ActionAllow:
		slog.Info("deep filter: allow — clearing warn",
			"user", fMsg.UserID, "msg_id", msgID, "reason", result.Reason)
		h.broadcast(roomID, &OutboundMessage{Type: "clear_warn", MsgID: msgID, UserID: userID})

	case filter.ActionBlock:
		slog.Info("deep filter: block — retracting message",
			"user", fMsg.UserID, "room", roomID,
			"msg_id", msgID, "reason", result.Reason, "score", result.Score)
		h.broadcast(roomID, &OutboundMessage{Type: "delete", MsgID: msgID, UserID: userID, Reason: result.Reason, Score: result.Score})

	default: // quarantine
		slog.Info("deep filter: quarantine — warning clients",
			"user", fMsg.UserID, "room", roomID,
			"msg_id", msgID, "reason", result.Reason)
		h.broadcast(roomID, &OutboundMessage{Type: "warn", MsgID: msgID, UserID: userID, Reason: result.Reason, Score: result.Score})
	}
}

// sendToClient sends a message to a single client by its internal client ID.
func (h *Hub) sendToClient(clientID string, out *OutboundMessage) {
	raw, err := json.Marshal(out)
	if err != nil {
		slog.Error("marshal sendToClient message", "err", err)
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, room := range h.rooms {
		if c, ok := room[clientID]; ok {
			select {
			case c.send <- raw:
			default:
				slog.Warn("slow client, block notification dropped", "client", clientID)
			}
			return
		}
	}
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

func newMsgID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
