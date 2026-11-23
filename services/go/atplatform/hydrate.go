package atplatform

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/CountableNewt/my-context-protocol/services/go/reposync"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

type revisionSource interface {
	ResolveCommitSHA(context.Context, string, string, string, string) (string, error)
	Fetch(context.Context, string, string, string, string) (reposync.Outcome, error)
}

// FetchRevision never resolves a moving default branch during cache recovery.
func (p *Platform) FetchRevision(ctx context.Context, connection, revision string) (reposync.Outcome, error) {
	if len(revision) != 40 && len(revision) != 64 {
		return reposync.Outcome{}, errors.New("source recovery requires an immutable revision")
	}
	if _, err := hex.DecodeString(revision); err != nil {
		return reposync.Outcome{}, errors.New("source recovery requires an immutable revision")
	}
	if p == nil || p.Raw == nil {
		return reposync.Outcome{}, errors.New("source platform is not configured")
	}
	r, err := p.Raw.Execute(ctx, turso.Statement{SQL: `SELECT r.provider,r.repo_owner,r.repo_name,r.token_encrypted,r.github_installation_id,a.github_token_encrypted,p.account_id FROM repo_connections r JOIN projects p ON p.id=r.project_id JOIN accounts a ON a.id=p.account_id WHERE r.id=?`, Args: []turso.Value{turso.Text(connection)}, WantRows: true})
	if err != nil {
		return reposync.Outcome{}, err
	}
	if r == nil || len(r.Rows) != 1 || len(r.Rows[0]) != 7 {
		return reposync.Outcome{}, errors.New("source connection not found")
	}
	row := r.Rows[0]
	value := func(i int) string {
		if row[i].Type == "null" {
			return ""
		}
		return fmt.Sprint(row[i].Value)
	}
	if value(0) == "tangled" {
		if p.Tangled == nil {
			return reposync.Outcome{}, errors.New("Tangled source recovery is not configured")
		}
		b, err := p.Raw.Execute(ctx, turso.Statement{SQL: `SELECT repository_did,actor_did FROM atproto_repo_bindings WHERE connection_id=?`, Args: []turso.Value{turso.Text(connection)}, WantRows: true})
		if err != nil {
			return reposync.Outcome{}, err
		}
		if b == nil || len(b.Rows) != 1 || len(b.Rows[0]) != 2 {
			return reposync.Outcome{}, errors.New("native source binding is missing")
		}
		linked, err := p.Accounts.Identities.ATProtoIdentity(ctx, value(6))
		if err != nil {
			return reposync.Outcome{}, err
		}
		if linked == nil || linked.DID != fmt.Sprint(b.Rows[0][1].Value) {
			return reposync.Outcome{}, errors.New("source actor must be reauthorized")
		}
		return p.Tangled.AuthenticatedRevision(ctx, linked.DID, fmt.Sprint(b.Rows[0][0].Value), revision)
	}
	if value(0) != "" && value(0) != "github" {
		return reposync.Outcome{}, errors.New("unsupported source provider")
	}
	token := ""
	for _, encrypted := range []string{value(3), value(5)} {
		if encrypted != "" {
			candidate, err := p.Accounts.Store.Cipher.Decrypt(encrypted)
			if err == nil && candidate != "" {
				token = candidate
				break
			}
		}
	}
	if installation := value(4); installation != "" {
		token, err = p.GitHub.InstallationToken(ctx, installation, token)
		if err != nil {
			return reposync.Outcome{}, err
		}
	}
	var fetcher revisionSource = p.githubSource
	if fetcher == nil {
		fetcher = reposync.Fetcher{}
	}
	resolved, err := fetcher.ResolveCommitSHA(ctx, value(1), value(2), revision, token)
	if err != nil {
		return reposync.Outcome{}, err
	}
	if !strings.EqualFold(resolved, revision) {
		return reposync.Outcome{}, errors.New("source revision changed during cache recovery")
	}
	snapshot, err := fetcher.Fetch(ctx, value(1), value(2), revision, token)
	if err == nil {
		snapshot.ResolvedCommitSHA = resolved
	}
	return snapshot, err
}
