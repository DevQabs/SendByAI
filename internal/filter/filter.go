// Package filter defines the moderation pipeline interface.
// Step 2 adds a HuggingFace classifier; Step 3 adds an Ollama deep-reasoning layer.
// Each step is a Filter implementation dropped into the Chain without changing hub code.
package filter

import "context"

// Action is the moderation decision for a message.
type Action int

const (
	ActionAllow      Action = iota // pass through unchanged
	ActionBlock                    // drop silently
	ActionQuarantine               // isolate for manual review
	ActionReplace                  // substitute sanitized content
)

// Message is the unit flowing through the filter pipeline.
type Message struct {
	RoomID  string
	UserID  string
	Content string
	Meta    map[string]any // arbitrary per-filter metadata
}

// Result is the decision produced by a Filter.
type Result struct {
	Action  Action
	Message *Message // populated for ActionAllow / ActionReplace
	Reason  string
	Score   float64 // toxicity score 0.0–1.0; 0 = clean
}

// Filter is the interface every moderation layer must satisfy.
// Implementations must be safe for concurrent use from multiple goroutines.
type Filter interface {
	Name() string
	Filter(ctx context.Context, msg *Message) (*Result, error)
}
