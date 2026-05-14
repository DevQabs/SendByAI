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
	for _, f := range c.filters {
		result, err := f.Filter(ctx, current)
		if err != nil {
			return nil, fmt.Errorf("filter %q: %w", f.Name(), err)
		}
		switch result.Action {
		case ActionBlock, ActionQuarantine:
			return result, nil
		case ActionReplace:
			current = result.Message
		}
	}
	return &Result{Action: ActionAllow, Message: current}, nil
}
