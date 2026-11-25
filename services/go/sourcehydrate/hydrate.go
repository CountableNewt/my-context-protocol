// Package sourcehydrate reconstructs private Git-backed caches from immutable
// repository snapshots. It never changes canonical metadata or active pointers.
package sourcehydrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/CountableNewt/my-context-protocol/services/go/compiler"
	"github.com/CountableNewt/my-context-protocol/services/go/domainrecords"
	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/reposync"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

var ErrSourceMismatch = errors.New("source does not match canonical provenance")
var ErrMissingProvenance = errors.New("immutable source provenance is required before hydration")
var revisionPattern = regexp.MustCompile(`^(?:[a-fA-F0-9]{40}|[a-fA-F0-9]{64})$`)
var checksumPattern = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

type Database interface {
	Execute(context.Context, turso.Statement) (*turso.Result, error)
	Batch(context.Context, []turso.Statement) ([]turso.Result, error)
}
type Hydrator struct {
	DB    Database
	Fetch func(context.Context, string, string) (reposync.Outcome, error)
	// MaxSkills bounds newly hydrated packages per request; subsequent calls
	// select only missing bodies/instructions and therefore skip completed work.
	MaxSkills int
}
type provenance struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Revision   string `json:"revision"`
	Checksum   string `json:"checksum"`
}
type item struct{ ID, PackageID, Path, Checksum, Canonical, PackageHash, PackagePath string }
type connection struct{ ID, Repository, NativeDID string }

func statement(sql string, args ...turso.Value) turso.Statement {
	return turso.Statement{SQL: sql, Args: args, WantRows: true}
}
func scalar(row []turso.Value, index int) string {
	if index >= len(row) || row[index].Type == "null" {
		return ""
	}
	return fmt.Sprint(row[index].Value)
}

// Hydrate reconstructs the manifest's target active release. Historical release
// views can call HydrateRelease explicitly without changing active_release_id.
func (h Hydrator) Hydrate(ctx context.Context, account, project string) error {
	if h.DB == nil {
		return errors.New("source cache database is required")
	}
	r, err := h.DB.Execute(ctx, statement("SELECT c.target_active_release_id,c.review_digest FROM domain_canonical_projects c JOIN projects p ON p.id=c.project_id WHERE c.project_id=? AND c.account_id=? AND p.account_id=?", turso.Text(project), turso.Text(account), turso.Text(account)))
	if err != nil {
		return err
	}
	if len(r.Rows) != 1 {
		return errors.New("source project is not owned by account")
	}
	release := scalar(r.Rows[0], 0)
	if release == "" {
		return nil
	}
	err = h.HydrateRelease(ctx, account, project, release)
	var pending *domainrecords.PendingError
	if errors.As(err, &pending) {
		pending.Digest = scalar(r.Rows[0], 1)
	}
	return err
}

func (h Hydrator) HydrateRelease(ctx context.Context, account, project, release string) error {
	if h.DB == nil {
		return errors.New("source cache database is required")
	}
	r, err := h.DB.Execute(ctx, statement("SELECT r.commit_sha FROM releases r JOIN projects p ON p.id=r.project_id WHERE r.id=? AND r.project_id=? AND p.account_id=?", turso.Text(release), turso.Text(project), turso.Text(account)))
	if err != nil {
		return err
	}
	if len(r.Rows) != 1 {
		return errors.New("source release is not owned by account")
	}
	revision := scalar(r.Rows[0], 0)
	missing := `(c.skill_body IS NULL OR c.canonical_json IS NULL OR CASE WHEN json_valid(c.canonical_json) THEN json_type(c.canonical_json,'$.instructions') IS NULL OR json_extract(c.canonical_json,'$.instructions')<>c.skill_body ELSE 1 END)`
	count, err := h.DB.Execute(ctx, statement("SELECT COUNT(*) FROM compiled_skills c WHERE c.release_id=? AND "+missing, turso.Text(release)))
	if err != nil {
		return err
	}
	if scalar(count.Rows[0], 0) == "0" {
		return nil
	}
	if !revisionPattern.MatchString(revision) {
		return ErrMissingProvenance
	}
	if h.Fetch == nil {
		return errors.New("immutable repository fetcher is required")
	}
	limit := h.MaxSkills
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := h.DB.Execute(ctx, statement("SELECT c.id,c.skill_package_id,c.path,c.source_checksum,CASE WHEN json_valid(c.canonical_json) THEN json_remove(c.canonical_json,'$.instructions') ELSE NULL END,p.hash,p.path FROM compiled_skills c JOIN skill_packages p ON p.id=c.skill_package_id WHERE c.release_id=? AND "+missing+" ORDER BY c.id LIMIT ?", turso.Text(release), turso.Integer(int64(limit))))
	if err != nil {
		return err
	}
	connections, err := h.connections(ctx, project)
	if err != nil {
		return err
	}
	snapshots := map[string]reposync.Outcome{}
	defer func() {
		for _, snapshot := range snapshots {
			_ = snapshot.Cleanup()
		}
	}()
	for _, row := range rows.Rows {
		if len(row) != 7 {
			return errors.New("invalid source metadata row")
		}
		v := item{scalar(row, 0), scalar(row, 1), scalar(row, 2), scalar(row, 3), scalar(row, 4), scalar(row, 5), scalar(row, 6)}
		var canonical map[string]json.RawMessage
		if json.Unmarshal([]byte(v.Canonical), &canonical) != nil || canonical == nil {
			return ErrMissingProvenance
		}
		var source provenance
		if json.Unmarshal(canonical["source"], &source) != nil {
			return ErrMissingProvenance
		}
		if !checksumPattern.MatchString(source.Checksum) || !strings.EqualFold(source.Revision, revision) || source.Path != v.Path || v.PackagePath != v.Path || v.Checksum != "" && !strings.EqualFold(v.Checksum, source.Checksum) || v.PackageHash != "" && !strings.EqualFold(v.PackageHash, source.Checksum) {
			return ErrSourceMismatch
		}
		var selected *connection
		for i := range connections {
			candidate := &connections[i]
			if source.Repository == candidate.Repository || candidate.NativeDID != "" && source.Repository == candidate.NativeDID {
				if selected != nil {
					return errors.New("ambiguous repository provenance")
				}
				selected = candidate
			}
		}
		if selected == nil && len(connections) == 1 && strings.TrimSpace(source.Repository) != "" {
			// Migration changes the fetch location, not historical provenance.
			// A unique current connection may supply copied history, but the exact
			// revision, path and checksum below must match before caching it.
			// The original canonical repository identity stays unchanged.
			selected = &connections[0]
		}
		if selected == nil {
			return ErrSourceMismatch
		}
		snapshot, ok := snapshots[selected.ID]
		if !ok {
			snapshot, err = h.Fetch(ctx, selected.ID, revision)
			if err != nil {
				return err
			}
			if !strings.EqualFold(snapshot.ResolvedCommitSHA, revision) || snapshot.RepositoryRoot == "" {
				_ = snapshot.Cleanup()
				return ErrSourceMismatch
			}
			snapshots[selected.ID] = snapshot
		}
		filename, err := safeSkillPath(snapshot.RepositoryRoot, v.Path)
		if err != nil {
			return err
		}
		skill, err := compiler.ReadSkill(filename, snapshot.RepositoryRoot)
		if err != nil {
			return err
		}
		if skill.Path != v.Path || !strings.EqualFold(skill.Hash, source.Checksum) {
			return ErrSourceMismatch
		}
		files, err := compiler.PackageFiles(ctx, filepath.Dir(filename))
		if err != nil {
			return err
		}
		if err = h.files(ctx, v, files); err != nil {
			return err
		}
		// Match the reviewed public source and canonical metadata. An intervening
		// record projection cannot attach this body to another source version.
		// Bind one base64 BLOB and cast it to UTF-8 text twice in SQL. This keeps
		// even a 1MiB quote/control-heavy body below Hrana's request byte limit.
		updated, err := h.DB.Execute(ctx, statement("UPDATE compiled_skills SET skill_body=CAST(?1 AS TEXT),canonical_json=json_set(canonical_json,'$.instructions',CAST(?1 AS TEXT)) WHERE id=?2 AND release_id=?3 AND skill_package_id=?4 AND path=?5 AND json_remove(canonical_json,'$.instructions')=?6 AND (source_checksum IS NULL OR lower(source_checksum)=lower(?7)) RETURNING id", turso.Blob([]byte(skill.Body)), turso.Text(v.ID), turso.Text(release), turso.Text(v.PackageID), turso.Text(v.Path), turso.Text(v.Canonical), turso.Text(source.Checksum)))
		if err != nil {
			return err
		}
		if len(updated.Rows) != 1 {
			return ErrSourceMismatch
		}
	}
	remaining, err := h.DB.Execute(ctx, statement("SELECT COUNT(*) FROM compiled_skills c WHERE c.release_id=? AND "+missing, turso.Text(release)))
	if err != nil {
		return err
	}
	if scalar(remaining.Rows[0], 0) != "0" {
		return &domainrecords.PendingError{Completed: len(rows.Rows), Total: len(rows.Rows) + intValue(remaining.Rows[0][0]), Digest: revision}
	}
	return nil
}
func intValue(v turso.Value) int { var n int; fmt.Sscan(fmt.Sprint(v.Value), &n); return n }
func (h Hydrator) connections(ctx context.Context, project string) ([]connection, error) {
	r, err := h.DB.Execute(ctx, statement("SELECT r.id,r.repo_owner,r.repo_name,b.repository_did FROM repo_connections r LEFT JOIN atproto_repo_bindings b ON b.connection_id=r.id WHERE r.project_id=? ORDER BY r.id LIMIT 11", turso.Text(project)))
	if err != nil {
		return nil, err
	}
	if len(r.Rows) > 10 {
		return nil, errors.New("too many repository connections")
	}
	out := []connection{}
	for _, row := range r.Rows {
		if len(row) != 4 {
			return nil, errors.New("invalid repository identity row")
		}
		out = append(out, connection{scalar(row, 0), scalar(row, 1) + "/" + scalar(row, 2), scalar(row, 3)})
	}
	return out, nil
}
func safeSkillPath(root, relative string) (string, error) {
	if relative == "" || relative != path.Clean(relative) || path.IsAbs(relative) || strings.ContainsAny(relative, "\\\x00\r\n") || relative == ".." || strings.HasPrefix(relative, "../") || path.Base(relative) != "SKILL.md" {
		return "", ErrSourceMismatch
	}
	current := root
	parts := strings.Split(relative, "/")
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", ErrSourceMismatch
		}
		if index < len(parts)-1 && !info.IsDir() || index == len(parts)-1 && !info.Mode().IsRegular() {
			return "", ErrSourceMismatch
		}
	}
	return current, nil
}
func (h Hydrator) files(ctx context.Context, v item, files []compiler.PackageFile) error {
	existing, err := h.DB.Execute(ctx, statement("SELECT path,checksum,byte_count FROM skill_package_files WHERE skill_package_id=?", turso.Text(v.PackageID)))
	if err != nil {
		return err
	}
	same := map[string]bool{}
	for _, row := range existing.Rows {
		if len(row) != 3 {
			return errors.New("invalid source cache file row")
		}
		for _, file := range files {
			if scalar(row, 0) == file.Path && scalar(row, 1) == file.Checksum && intValue(row[2]) == len(file.Content) {
				same[file.Path] = true
			}
		}
	}
	batch := []turso.Statement{}
	paths := []turso.Value{turso.Text(v.PackageID)}
	for _, file := range files {
		paths = append(paths, turso.Text(file.Path))
		if same[file.Path] {
			continue
		}
		id, err := identity.UUID()
		if err != nil {
			return err
		}
		contentType := turso.Null()
		if file.ContentType != "" {
			contentType = turso.Text(file.ContentType)
		}
		batch = append(batch, statement("INSERT INTO skill_package_files(id,skill_package_id,path,content,content_type,byte_count,checksum) VALUES(?,?,?,?,?,?,?) ON CONFLICT(skill_package_id,path) DO UPDATE SET content=excluded.content,content_type=excluded.content_type,byte_count=excluded.byte_count,checksum=excluded.checksum", turso.Text(id), turso.Text(v.PackageID), turso.Text(file.Path), turso.Blob(file.Content), contentType, turso.Integer(int64(len(file.Content))), turso.Text(file.Checksum)))
		if len(batch) >= 8 {
			if _, err = h.DB.Batch(ctx, batch); err != nil {
				return err
			}
			batch = nil
		}
	}
	if len(batch) > 0 {
		if _, err = h.DB.Batch(ctx, batch); err != nil {
			return err
		}
	}
	query := "DELETE FROM skill_package_files WHERE skill_package_id=?"
	if len(paths) > 1 {
		query += " AND path NOT IN(" + strings.TrimSuffix(strings.Repeat("?,", len(paths)-1), ",") + ")"
	}
	_, err = h.DB.Execute(ctx, statement(query, paths...))
	return err
}
