package ataccount

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/CountableNewt/my-context-protocol/services/go/atidentity"
	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"github.com/bluesky-social/indigo/atproto/atcrypto"
	"github.com/bluesky-social/indigo/atproto/auth/oauth"
)

type FullDatabase interface {
	Database
	Batch(context.Context, []turso.Statement) ([]turso.Result, error)
}

// FromEnvironment uses the same client metadata and encrypted OAuth store as
// identity. No sessions or schema are created during service startup.
func FromEnvironment(db FullDatabase, getenv func(string) string) (*Manager, *oauth.ClientApp, error) {
	if !strings.EqualFold(getenv("ATPROTO_ENABLED"), "true") && getenv("ATPROTO_ENABLED") != "1" {
		return nil, nil, nil
	}
	cipher, err := security.NewTokenCipher(getenv("ENCRYPTION_KEY"))
	if err != nil {
		return nil, nil, err
	}
	key, err := atcrypto.ParsePrivateMultibase(getenv("ATPROTO_OAUTH_PRIVATE_KEY"))
	if err != nil {
		return nil, nil, errors.New("invalid ATProto OAuth assertion key")
	}
	collections := []string{}
	for _, v := range strings.Split(getenv("ATPROTO_RECORD_COLLECTIONS"), ",") {
		if v = strings.TrimSpace(v); v != "" {
			collections = append(collections, v)
		}
	}
	var permissions []atidentity.ServicePermission
	if raw := strings.TrimSpace(getenv("ATPROTO_SERVICE_PERMISSIONS")); raw != "" {
		if json.Unmarshal([]byte(raw), &permissions) != nil {
			return nil, nil, errors.New("invalid ATProto service permissions")
		}
	}
	store := &atidentity.Store{DB: db, Cipher: cipher}
	app, _, err := atidentity.NewClient(atidentity.ClientConfig{ClientID: getenv("ATPROTO_CLIENT_ID"), CallbackURL: getenv("ATPROTO_CALLBACK_URL"), Collections: collections, PrivateKey: key, KeyID: getenv("ATPROTO_OAUTH_KEY_ID"), ServiceAuth: permissions}, store)
	if err != nil {
		return nil, nil, err
	}
	return &Manager{DB: db, Identities: identity.ATProtoSQLStore{DB: db, Cipher: cipher}, Sessions: app, Store: store, Directory: app.Dir}, app, nil
}
