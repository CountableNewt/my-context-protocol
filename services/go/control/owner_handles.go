package control

import (
	"context"
	"github.com/bluesky-social/indigo/atproto/syntax"
	"time"
)

type ownerHandleLookup struct {
	ctx    context.Context
	lookup func(context.Context, string) (string, error)
	values map[string]string
	cancel context.CancelFunc
}

func (h *ownerHandleLookup) close() {
	if h.cancel != nil {
		h.cancel()
	}
}
func (h *ownerHandleLookup) get(did string) string {
	if h.lookup == nil {
		return ""
	}
	if handle, ok := h.values[did]; ok {
		return handle
	}
	if h.cancel == nil {
		h.ctx, h.cancel = context.WithTimeout(h.ctx, 2*time.Second)
	}
	h.values[did] = ""
	if h.ctx.Err() != nil {
		return ""
	}
	handle, err := h.lookup(h.ctx, did)
	parsed, parseErr := syntax.ParseHandle(handle)
	if err == nil && parseErr == nil && !parsed.IsInvalidHandle() {
		h.values[did] = parsed.Normalize().String()
	}
	return h.values[did]
}
func (s *Service) projectResponseWithOwner(p row, handles *ownerHandleLookup) row {
	result := s.projectResponse(p)
	if connection, ok := result["repo_connection"].(row); ok && str(connection["provider"]) == "tangled" {
		if handle := handles.get(str(connection["repo_owner"])); handle != "" {
			connection["owner_handle"] = handle
		}
	}
	return result
}
