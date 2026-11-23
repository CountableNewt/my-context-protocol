package mcp

import (
	"context"
	"errors"
	"github.com/CountableNewt/my-context-protocol/services/go/domainrecords"
)

func (s *Service) telemetryPublication(ctx context.Context, err error) string {
	if err == nil {
		return ""
	}
	marker, ok := s.store.(interface{ CanonicalTelemetry(context.Context) bool })
	if !ok || !marker.CanonicalTelemetry(ctx) {
		return ""
	}
	if errors.Is(err, domainrecords.ErrPending) {
		return "pending"
	}
	return "failed"
}
