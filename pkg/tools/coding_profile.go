package tools

import (
	"context"

	"github.com/sipeed/picoclaw/pkg/media"
)

// codingAliasTool exposes an existing tool under one of the minimal coding
// profile's four primary names. It delegates all behavior to the underlying
// tool and adds no new functionality.
type codingAliasTool struct {
	name  string
	inner Tool
}

func NewCodingAliasTool(name string, inner Tool) Tool {
	return &codingAliasTool{name: name, inner: inner}
}

func (t *codingAliasTool) Name() string { return t.name }

func (t *codingAliasTool) Description() string { return t.inner.Description() }

func (t *codingAliasTool) Parameters() map[string]any { return t.inner.Parameters() }

func (t *codingAliasTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	return t.inner.Execute(ctx, args)
}

func (t *codingAliasTool) SetMediaStore(store media.MediaStore) {
	if aware, ok := t.inner.(mediaStoreAware); ok {
		aware.SetMediaStore(store)
	}
}
