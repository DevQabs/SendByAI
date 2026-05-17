package filter

import (
	"context"
	"fmt"
)

// Chain executes filters sequentially.
// On Block or Quarantine the chain short-circuits and returns immediately.
// On Replace, the modified message continues to the next filter.
type Chain struct {
	filters []Filter
}

// NewChain composes multiple filters into a single pipeline.
func NewChain(filters ...Filter) *Chain {
	return &Chain{filters: filters}
}

func (c *Chain) Filter(ctx context.Context, msg *Message) (*Result, error) {
	current := msg
	var pendingQuarantine *Result

	for _, f := range c.filters {
		result, err := f.Filter(ctx, current)
		if err != nil {
			return nil, fmt.Errorf("filter %q: %w", f.Name(), err)
		}
		switch result.Action {
		case ActionBlock:
			return result, nil
		case ActionQuarantine:
			pendingQuarantine = result
			// propagate quarantine signal so downstream filters can re-judge
			if current.Meta == nil {
				current.Meta = make(map[string]any)
			}
			current.Meta["quarantined"] = true
			current.Meta["quarantine_reason"] = result.Reason
			current.Meta["quarantine_score"] = result.Score
		case ActionAllow:
			pendingQuarantine = nil // downstream filter cleared the quarantine
			current = result.Message
		case ActionReplace:
			pendingQuarantine = nil
			current = result.Message
		}
	}

	if pendingQuarantine != nil {
		return pendingQuarantine, nil
	}
	return &Result{Action: ActionAllow, Message: current}, nil
}
