package githubapp

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"go.yaml.in/yaml/v3"
)

var skillID = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

const sidecarPath = ".mycontext/skills.yaml"

func MergeSidecar(existing []byte, document map[string]any, exposure string) ([]byte, error) {
	if len(existing) > 256<<10 || !utf8.Valid(existing) {
		return nil, errors.New("existing skill policy is oversized or invalid UTF-8")
	}
	id, _ := document["id"].(string)
	if !skillID.MatchString(id) || len(id) > 64 {
		return nil, errors.New("invalid canonical skill id")
	}
	if exposure != "tool" && exposure != "resource" && exposure != "prompt" {
		return nil, errors.New("invalid canonical exposure")
	}
	validation, _ := document["validation"].(map[string]any)
	if validation == nil || validation["clarificationRequired"] != false {
		return nil, &PublicError{409, "Resolve runtime clarification questions before writeback", nil}
	}
	root := map[string]any{}
	if len(existing) > 0 {
		decoder := yaml.NewDecoder(strings.NewReader(string(existing)))
		if err := decoder.Decode(&root); err != nil {
			return nil, errors.New("existing policy YAML is invalid")
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return nil, errors.New("existing policy must be one YAML document")
		}
		if root == nil {
			return nil, errors.New("existing policy root must be a mapping")
		}
		if version, ok := root["version"]; ok && version != 1 {
			return nil, errors.New("unsupported existing policy version")
		}
	}
	skills := map[string]any{}
	if existing, ok := root["skills"]; ok {
		var valid bool
		skills, valid = existing.(map[string]any)
		if !valid {
			return nil, errors.New("existing policy skills must be a mapping")
		}
	}
	source, _ := document["source"].(map[string]any)
	checksum, _ := source["checksum"].(string)
	if decoded, err := hex.DecodeString(checksum); err != nil || len(decoded) != 32 {
		return nil, errors.New("canonical checksum is missing")
	}
	metadata := map[string]any{"exposure": exposure}
	for _, key := range []string{"kind", "scope", "activation", "enforcement", "priority", "requires", "version", "lifecycle"} {
		if value, ok := document[key]; ok && value != nil {
			metadata[key] = value
		}
	}
	if value, ok := document["avoidWhen"]; ok {
		metadata["avoid_when"] = value
	}
	if value, ok := document["conflictsWith"]; ok {
		metadata["conflicts_with"] = value
	}
	generated := map[string]any{"base_checksum": checksum, "metadata": metadata}
	prior, _ := skills[id].(map[string]any)
	skills[id] = mergeMapping(prior, generated)
	root["version"] = 1
	root["skills"] = skills
	result, err := yaml.Marshal(root)
	if err != nil {
		return nil, err
	}
	if len(result) > 256<<10 {
		return nil, errors.New("merged policy exceeds 256 KiB")
	}
	return result, nil
}
func mergeMapping(existing, generated map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range existing {
		out[k] = v
	}
	for k, v := range generated {
		if nested, ok := v.(map[string]any); ok {
			prior, _ := out[k].(map[string]any)
			out[k] = mergeMapping(prior, nested)
		} else {
			out[k] = v
		}
	}
	return out
}
func (s *Service) Writeback(ctx context.Context, account, project, release, compiled string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := s.ownedProject(ctx, account, project); err != nil {
		return nil, err
	}
	unlock, err := s.lock(ctx, project)
	if err != nil {
		return nil, err
	}
	defer unlock()
	r, err := s.config.DB.Execute(ctx, sql("SELECT c.canonical_json,c.exposure_type FROM compiled_skills c JOIN releases r ON r.id=c.release_id WHERE c.id=? AND c.release_id=? AND r.project_id=?", turso.Text(compiled), turso.Text(release), turso.Text(project)))
	if err != nil {
		return nil, err
	}
	if len(r.Rows) != 1 {
		return nil, &PublicError{404, "Compiled skill not found", nil}
	}
	var document map[string]any
	if json.Unmarshal([]byte(col(r.Rows[0], 0)), &document) != nil {
		return nil, &PublicError{409, "Canonical skill metadata is unavailable", nil}
	}
	exposure := col(r.Rows[0], 1)
	// Validate before creating any external branch or PR.
	if _, err = MergeSidecar(nil, document, exposure); err != nil {
		return nil, &PublicError{409, err.Error(), nil}
	}
	connection, err := s.config.DB.Execute(ctx, sql("SELECT repo_owner,repo_name,default_branch,github_installation_id,token_encrypted,id FROM repo_connections WHERE project_id=? AND COALESCE(NULLIF(provider,''),'github')='github' ORDER BY id LIMIT 1", turso.Text(project)))
	if err != nil {
		return nil, err
	}
	if len(connection.Rows) != 1 {
		return nil, &PublicError{409, "No repository is connected", nil}
	}
	row := connection.Rows[0]
	owner, repo, base := col(row, 0), col(row, 1), col(row, 2)
	api, err := reposURL(owner, repo, base)
	if err != nil {
		return nil, err
	}
	oauth := ""
	if encrypted := col(row, 4); encrypted != "" {
		oauth, _ = s.config.Cipher.Decrypt(encrypted)
	}
	if oauth == "" {
		oauth, _, err = s.accountToken(ctx, account)
		if err != nil && col(row, 3) == "" {
			return nil, err
		}
	}
	token := oauth
	if installation := col(row, 3); installation != "" {
		token, err = s.InstallationToken(ctx, installation, oauth)
		if err != nil {
			return nil, err
		}
	}
	if token == "" {
		return nil, &PublicError{401, "Repository write access is not configured", nil}
	}
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err = s.config.Client.api(ctx, "GET", api+"/git/ref/heads/"+url.PathEscape(base), token, "default branch lookup", nil, &ref, 200); err != nil {
		return nil, err
	}
	if ref.Object.SHA == "" {
		return nil, errors.New("default branch SHA missing")
	}
	data, _, status, err := s.config.Client.request(ctx, "GET", api+"/contents/"+sidecarPath+"?ref="+url.QueryEscape(base), token, nil)
	if err != nil {
		return nil, err
	}
	var existing []byte
	var existingSHA string
	if status == 200 {
		var file struct{ SHA, Content, Encoding string }
		if json.Unmarshal(data, &file) != nil || (file.Encoding != "" && file.Encoding != "base64") {
			return nil, errors.New("existing sidecar response is invalid")
		}
		existing, err = base64.StdEncoding.DecodeString(strings.Join(strings.Fields(file.Content), ""))
		if err != nil {
			return nil, errors.New("existing sidecar is not base64")
		}
		existingSHA = file.SHA
		if existingSHA == "" {
			return nil, errors.New("existing sidecar SHA missing")
		}
	} else if status != 404 {
		return nil, &APIError{status, "sidecar read"}
	}
	merged, err := MergeSidecar(existing, document, exposure)
	if err != nil {
		return nil, &PublicError{409, err.Error(), nil}
	}
	random, err := identity.UUID()
	if err != nil {
		return nil, err
	}
	id := document["id"].(string)
	short := id
	if len(short) > 32 {
		short = short[:32]
	}
	branch := "mcp/skill-metadata-" + short + "-" + strings.ToLower(random)
	if err = s.config.Client.api(ctx, "POST", api+"/git/refs", token, "metadata branch creation", map[string]any{"ref": "refs/heads/" + branch, "sha": ref.Object.SHA}, nil, 201); err != nil {
		return nil, err
	}
	update := map[string]any{"message": "chore: update portable runtime policy for " + id, "content": base64.StdEncoding.EncodeToString(merged), "branch": branch}
	if existingSHA != "" {
		update["sha"] = existingSHA
	}
	if err = s.config.Client.api(ctx, "PUT", api+"/contents/"+sidecarPath, token, "metadata write", update, nil, 200, 201); err != nil {
		return nil, err
	}
	var pull struct {
		URL string `json:"html_url"`
	}
	if err = s.config.Client.api(ctx, "POST", api+"/pulls", token, "draft pull request creation", map[string]any{"title": "Update portable runtime policy for " + id, "head": branch, "base": base, "body": "Updates `.mycontext/skills.yaml` from validated MyContextProtocol deployment metadata. `SKILL.md` remains the authored source and is not reconstructed.", "draft": true}, &pull, 201); err != nil {
		return nil, err
	}
	parsed, err := url.Parse(pull.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.User != nil || !strings.HasPrefix(parsed.Path, "/"+owner+"/"+repo+"/pull/") {
		return nil, errors.New("invalid GitHub pull request URL")
	}
	if _, err = s.config.DB.Execute(ctx, sql("UPDATE skill_runtime_overrides SET writeback_pr_url=? WHERE project_id=? AND skill_id=? AND (repo_connection_id IS NULL OR repo_connection_id=?)", turso.Text(pull.URL), turso.Text(project), turso.Text(id), turso.Text(col(row, 5)))); err != nil {
		return nil, err
	}
	return map[string]any{"pull_request_url": pull.URL, "branch": branch, "source_path": sidecarPath}, nil
}
