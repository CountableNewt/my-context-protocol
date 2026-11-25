package xrpcauth

import (
	"errors"
	"strings"

	"github.com/CountableNewt/my-context-protocol/services/go/atidentity"
	"github.com/bluesky-social/indigo/atproto/syntax"
)

func FromEnvironment(db Database, getenv func(string) string) (*Verifier, error) {
	if getenv("ATPROTO_ENABLED") != "1" && !strings.EqualFold(getenv("ATPROTO_ENABLED"), "true") {
		return nil, nil
	}
	audience := strings.TrimSpace(getenv("ATPROTO_SERVICE_DID"))
	parts := strings.Split(audience, "#")
	if len(parts) != 2 || parts[1] == "" {
		return nil, errors.New("ATPROTO_SERVICE_DID must be the deployment's DID#service reference")
	}
	if _, err := syntax.ParseDID(parts[0]); err != nil {
		return nil, errors.New("invalid ATProto service DID")
	}
	return &Verifier{Audience: audience, Directory: atidentity.PublicDirectory(), DB: db}, nil
}
