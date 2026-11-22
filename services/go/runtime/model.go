// Package runtime resolves compiled skill policy without process-local state.
package runtime

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"
)

type Activation struct {
	Mode     string   `json:"mode"`
	Intents  []string `json:"intents"`
	Events   []string `json:"events"`
	Tags     []string `json:"tags"`
	Examples []string `json:"examples"`
}
type Requirement struct {
	Capability string `json:"capability"`
	Required   bool   `json:"required"`
	OnMissing  string `json:"on_missing"`
}

func (r *Requirement) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return errors.New("invalid requirement")
	}
	if v, ok := fields["required"]; !ok || string(v) != "true" && string(v) != "false" {
		return errors.New("invalid requirement")
	}
	type raw Requirement
	var value struct {
		raw
		Legacy string `json:"onMissing"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*r = Requirement(value.raw)
	if r.OnMissing == "" {
		r.OnMissing = value.Legacy
	}
	return nil
}

type Source struct {
	Repository *string `json:"repository,omitempty"`
	Path       string  `json:"path"`
	Revision   *string `json:"revision,omitempty"`
	Checksum   string  `json:"checksum"`
}
type Validation struct {
	ClarificationRequired bool     `json:"clarificationRequired"`
	MissingFields         []string `json:"missingFields"`
	Warnings              []string `json:"warnings"`
}
type Document struct {
	SchemaVersion           int           `json:"schemaVersion"`
	ID                      string        `json:"id"`
	Name                    string        `json:"name"`
	Description             string        `json:"description"`
	Kind                    string        `json:"kind"`
	Scope                   string        `json:"scope"`
	Activation              Activation    `json:"activation"`
	AvoidWhen               []string      `json:"avoidWhen,omitempty"`
	Enforcement             string        `json:"enforcement"`
	Priority                int           `json:"priority"`
	Requires                []Requirement `json:"requires"`
	ConflictsWith           []string      `json:"conflictsWith"`
	Instructions            string        `json:"instructions"`
	Source                  Source        `json:"source"`
	Version                 string        `json:"version"`
	Lifecycle               *string       `json:"lifecycle,omitempty"`
	StandardFrontmatterJSON *string       `json:"standardFrontmatterJson,omitempty"`
	Validation              Validation    `json:"validation"`
}

func ParseDocument(raw string) (*Document, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil {
		return nil, errors.New("invalid canonical document")
	}
	for _, key := range []string{"schemaVersion", "id", "name", "description", "kind", "scope", "activation", "enforcement", "priority", "requires", "conflictsWith", "instructions", "source", "version", "validation"} {
		value, ok := fields[key]
		if !ok || string(value) == "null" {
			return nil, errors.New("incomplete canonical document")
		}
	}
	for _, key := range []string{"requires", "conflictsWith"} {
		if len(fields[key]) == 0 || fields[key][0] != '[' {
			return nil, errors.New("invalid canonical lists")
		}
	}
	var source map[string]json.RawMessage
	if json.Unmarshal(fields["source"], &source) != nil {
		return nil, errors.New("invalid source provenance")
	}
	for _, key := range []string{"path", "checksum"} {
		value, ok := source[key]
		if !ok || len(value) == 0 || value[0] != '"' {
			return nil, errors.New("incomplete source provenance")
		}
	}
	var activationFields map[string]json.RawMessage
	if json.Unmarshal(fields["activation"], &activationFields) != nil {
		return nil, errors.New("invalid activation")
	}
	for _, key := range []string{"intents", "events", "tags", "examples"} {
		if value, ok := activationFields[key]; ok {
			if !stringList(value) {
				return nil, errors.New("invalid activation list")
			}
		}
	}
	if !stringList(fields["conflictsWith"]) {
		return nil, errors.New("invalid conflict list")
	}
	var validation map[string]json.RawMessage
	if json.Unmarshal(fields["validation"], &validation) != nil {
		return nil, errors.New("invalid validation state")
	}
	for _, key := range []string{"clarificationRequired", "missingFields", "warnings"} {
		value, ok := validation[key]
		if !ok || string(value) == "null" {
			return nil, errors.New("incomplete validation state")
		}
	}
	var d Document
	if json.Unmarshal([]byte(raw), &d) != nil || d.SchemaVersion != 1 || d.ID == "" || d.Version == "" || !oneOf(d.Kind, "operating", "task", "tool-use", "reference") || !oneOf(d.Scope, "global", "organization", "workspace", "repository", "task") || !oneOf(d.Activation.Mode, "always", "intent", "event", "explicit") || !oneOf(d.Enforcement, "advisory", "required") {
		return nil, errors.New("invalid canonical document")
	}
	for _, r := range d.Requires {
		if r.Capability == "" || !oneOf(r.OnMissing, "fail_activation", "warn", "return_draft", "request_provider_selection", "continue_without_action") {
			return nil, errors.New("invalid capability requirement")
		}
	}
	return &d, nil
}
func oneOf(value string, values ...string) bool {
	for _, v := range values {
		if value == v {
			return true
		}
	}
	return false
}

type Hints struct {
	UseWhen      []string `json:"use_when,omitempty"`
	AvoidWhen    []string `json:"avoid_when,omitempty"`
	FailureModes []string `json:"failure_modes,omitempty"`
	InvokeFirst  bool     `json:"invoke_first,omitempty"`
}
type Skill struct {
	Hints     Hints
	ID        string
	ReleaseID string
	PackageID string
	Name      string
	Path      string
	Summary   string
	Body      string
	SkillID   string
	Version   string
	Checksum  string
	Document  *Document
}
type Capability struct {
	Name    string
	Type    string
	Schema  json.RawMessage
	SkillID string
}
type Assignment struct {
	SkillID        string
	Scope          string
	ActivationMode string
	TargetType     string
	TargetID       string
	Required       bool
	Priority       int
}
type Settings struct {
	TelemetryEnabled             bool
	TelemetryRetentionDays       int
	FeedbackIssueCreationEnabled bool
	LegacyCompiledTools          bool
}
type Snapshot struct {
	ReleaseStatus   string
	Subdomain       string
	CustomDomain    string
	ProjectID       string
	ProjectName     string
	ReleaseID       string
	Skills          []Skill
	Capabilities    []Capability
	Assignments     []Assignment
	Settings        Settings
	CatalogOverride string
}
type File struct {
	Path        string
	Checksum    string
	MediaType   string
	ByteCount   int
	Content     []byte
	ResourceURI string
}

func (s Snapshot) FindSkill(id, version string) (*Skill, error) {
	for _, skill := range s.Skills {
		if skill.SkillID == id && (version == "" || skill.Version == version) && skill.Document != nil {
			return &skill, nil
		}
	}
	return nil, errors.New("The requested skill or version is not active in this project")
}
func SafePath(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > 1024 || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "~") || strings.Contains(value, "\\") || !utf8.ValidString(value) {
		return "", errors.New("path must be a safe package-relative path")
	}
	decoded, err := url.PathUnescape(value)
	if err != nil || decoded != value {
		return "", errors.New("path must not contain percent-encoded traversal or separators")
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("path must be a safe package-relative path")
		}
		for _, c := range part {
			if c < 32 || c == 127 {
				return "", errors.New("path must be a safe package-relative path")
			}
		}
	}
	return value, nil
}
func ResourceURI(id, path, version string) string {
	uri := "ctx://skill/" + url.PathEscape(id)
	if path != "" {
		parts := strings.Split(path, "/")
		for i := range parts {
			parts[i] = url.PathEscape(parts[i])
		}
		uri += "/file/" + strings.Join(parts, "/")
	}
	if version != "" {
		uri += "?version=" + strings.ReplaceAll(url.QueryEscape(version), "+", "%20")
	}
	return uri
}

type ResourceReference struct {
	SkillID string
	Path    string
	Version string
}

func ParseResourceURI(raw string) (*ResourceReference, error) {
	if !strings.HasPrefix(raw, "ctx://skill/") {
		return nil, nil
	}
	if len(raw) > 4096 || strings.Contains(raw, "#") {
		return nil, errors.New("Invalid skill resource URI")
	}
	parts := strings.SplitN(raw, "?", 2)
	version := ""
	if len(parts) == 2 {
		q, err := url.ParseQuery(parts[1])
		if err != nil || len(q) != 1 || len(q["version"]) != 1 || !strings.HasPrefix(parts[1], "version=") {
			return nil, errors.New("Invalid skill resource URI")
		}
		version = strings.TrimSpace(q.Get("version"))
		if version == "" || len(version) > 512 {
			return nil, errors.New("Invalid skill resource URI")
		}
	}
	pathParts := strings.Split(strings.TrimPrefix(parts[0], "ctx://skill/"), "/file/")
	if len(pathParts) > 2 {
		return nil, errors.New("Invalid skill resource URI")
	}
	id, err := url.PathUnescape(pathParts[0])
	if err != nil || id == "" || strings.ContainsAny(id, "/\\\r\n\x00") {
		return nil, errors.New("Invalid skill resource URI")
	}
	ref := &ResourceReference{SkillID: id, Version: version}
	if len(pathParts) == 2 {
		segments := strings.Split(pathParts[1], "/")
		for i, v := range segments {
			decoded, e := url.PathUnescape(v)
			if e != nil || strings.ContainsAny(decoded, "/\\") {
				return nil, errors.New("Invalid skill resource URI")
			}
			segments[i] = decoded
		}
		ref.Path, err = SafePath(strings.Join(segments, "/"))
		if err != nil {
			return nil, err
		}
	}
	return ref, nil
}

func stringList(raw json.RawMessage) bool {
	if len(raw) == 0 || raw[0] != '[' {
		return false
	}
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return false
	}
	for _, value := range values {
		if len(value) == 0 || value[0] != '"' {
			return false
		}
	}
	return true
}
