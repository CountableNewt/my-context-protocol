package domainrecords

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

func (e Engine) versionCache(ctx context.Context, review Review) (map[string]atproto.Reference, error) {
	out := map[string]atproto.Reference{}
	last := ""
	for {
		r, err := e.DB.Execute(ctx, statement("SELECT collection,rkey,uri,cid FROM domain_record_versions v WHERE owner_did=? AND project_id=? AND (generation=? OR generation=(SELECT review_digest FROM domain_canonical_projects WHERE project_id=?)) AND rkey>? AND NOT EXISTS(SELECT 1 FROM domain_record_retirements x WHERE x.owner_did=v.owner_did AND x.collection=v.collection AND x.rkey=v.rkey AND x.deleting=1) ORDER BY rkey LIMIT 1000", turso.Text(review.OwnerDID), turso.Text(review.ProjectID), turso.Text(review.Digest), turso.Text(review.ProjectID), turso.Text(last)))
		if err != nil {
			return nil, err
		}
		for _, row := range r.Rows {
			if len(row) != 4 {
				return nil, errors.New("invalid version cache row")
			}
			collection, key, uri, cid := fmt.Sprint(row[0].Value), fmt.Sprint(row[1].Value), fmt.Sprint(row[2].Value), fmt.Sprint(row[3].Value)
			if uri != "at://"+review.OwnerDID+"/"+collection+"/"+key {
				return nil, errors.New("invalid version cache owner")
			}
			out[collection+"/"+key] = atproto.Reference{URI: uri, CID: cid}
			last = key
		}
		if len(r.Rows) < 1000 {
			return out, nil
		}
	}
}
func (e Engine) cacheVersion(ctx context.Context, review Review, collection, key string, ref atproto.Reference) error {
	_, err := e.DB.Execute(ctx, statement("INSERT INTO domain_record_versions(owner_did,project_id,collection,rkey,uri,cid,generation) VALUES(?,?,?,?,?,?,?) ON CONFLICT(owner_did,collection,rkey) DO UPDATE SET generation=excluded.generation WHERE domain_record_versions.uri=excluded.uri AND domain_record_versions.cid=excluded.cid", turso.Text(review.OwnerDID), turso.Text(review.ProjectID), turso.Text(collection), turso.Text(key), turso.Text(ref.URI), turso.Text(ref.CID), turso.Text(review.Digest)))
	return err
}
func refDigest(refs []atproto.Reference) string {
	var s strings.Builder
	for _, ref := range refs {
		s.WriteString(ref.URI)
		s.WriteByte(0)
		s.WriteString(ref.CID)
		s.WriteByte(0)
	}
	return digest([]byte(s.String()))
}
