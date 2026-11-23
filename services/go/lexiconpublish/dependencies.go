package lexiconpublish

import (
	"embed"

	"github.com/bluesky-social/indigo/atproto/lexicon"
)

// Official reference dependency copied verbatim from the repository's pinned
// indigo SDK v0.0.0-20260929184902-b2619d864df0 lexicons/com/atproto/repo/strongRef.json.
// It is validation-only and is never republished under the product authority.
//
//go:embed dependencies/*.json
var dependencies embed.FS

func validationCatalog() (*lexicon.BaseCatalog, error) {
	catalog := lexicon.NewBaseCatalog()
	if err := catalog.LoadEmbedFS(dependencies); err != nil {
		return nil, err
	}
	return catalog, nil
}
