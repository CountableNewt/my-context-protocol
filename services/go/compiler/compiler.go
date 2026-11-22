// Package compiler preserves the gateway's portable SKILL.md compilation format.
package compiler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

const MaxSkillBytes = 1 << 20

var slug = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var reserved = map[string]bool{"resolve_context": true, "get_skill": true, "report_skill_usage": true, "report_skill_feedback": true, "mycontext_catalog": true, "discover_skills": true, "list_capabilities": true}

type Issue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}
type Report struct {
	Valid    bool    `json:"is_valid"`
	Errors   []Issue `json:"errors"`
	Warnings []Issue `json:"warnings"`
}
type Skill struct {
	Path, Name, Description, Body, Hash string
	Frontmatter                         bool
	Metadata                            map[string]any
}
type Compiled struct {
	Files                                        []PackageFile
	Skill                                        Skill
	Summary, Exposure, Risk, SideEffects, Status string
	RepoSpecific                                 bool
	Schema                                       map[string]any
	Canonical                                    map[string]any
	Clarifications                               []map[string]any
	UseWhen, AvoidWhen, FailureModes             []string
	InvokeFirst                                  *bool
	SourcePolicyJSON, SourcePolicyChecksum       string
	SourcePolicyStale                            bool
}
type Policy struct {
	BaseChecksum string         `json:"base_checksum"`
	Metadata     map[string]any `json:"metadata"`
	Raw          string         `json:"-"`
}

func Hash(data []byte) string { s := sha256.Sum256(data); return hex.EncodeToString(s[:]) }
func ReadSkill(filename, root string) (Skill, error) {
	info, err := os.Lstat(filename)
	if err != nil {
		return Skill{}, err
	}
	if !info.Mode().IsRegular() {
		return Skill{}, errors.New("SKILL.md must be a regular file")
	}
	if info.Size() > MaxSkillBytes {
		return Skill{}, errors.New("SKILL.md exceeds size limit")
	}
	rel, err := filepath.Rel(root, filename)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Skill{}, errors.New("SKILL.md escapes repository")
	}
	file, err := os.Open(filename)
	if err != nil {
		return Skill{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxSkillBytes+1))
	if err != nil {
		return Skill{}, err
	}
	return Parse(filepath.ToSlash(rel), data)
}
func Parse(relative string, data []byte) (Skill, error) {
	s := Skill{Path: relative, Hash: Hash(data), Metadata: map[string]any{}}
	if len(data) > MaxSkillBytes {
		return s, errors.New("SKILL.md exceeds size limit")
	}
	if !utf8.Valid(data) {
		return s, errors.New("SKILL.md is not UTF-8")
	}
	content := string(data)
	if strings.TrimSpace(content) == "" {
		return s, errors.New("SKILL.md is empty")
	}
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n"), "\n")
	s.Body = content
	if strings.TrimSpace(lines[0]) == "---" {
		s.Frontmatter = true
		end := -1
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				end = i
				break
			}
		}
		if end < 0 {
			return s, errors.New("SKILL.md frontmatter is missing closing --- delimiter")
		}
		if err := decodeYAML([]byte(strings.Join(lines[1:end], "\n")), &s.Metadata); err != nil {
			return s, fmt.Errorf("invalid YAML frontmatter: %w", err)
		}
		if s.Metadata == nil {
			s.Metadata = map[string]any{}
		}
		s.Body = strings.Join(lines[end+1:], "\n")
		s.Name = scalar(s.Metadata["name"])
		if s.Name == "" {
			return s, errors.New("SKILL.md frontmatter is missing required name field")
		}
	} else {
		s.Name = path.Base(path.Dir(relative))
		if s.Name == "." || s.Name == "/" {
			return s, errors.New("cannot infer skill name without parent directory")
		}
	}
	s.Description = scalar(s.Metadata["description"])
	return s, nil
}
func decodeYAML(data []byte, target any) error {
	d := yaml.NewDecoder(bytes.NewReader(data))
	if err := d.Decode(target); err != nil && err != io.EOF {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("expected one YAML document")
	}
	return nil
}
func scalar(v any) string {
	switch v := v.(type) {
	case string:
		return strings.ReplaceAll(strings.TrimSpace(v), "\r", "")
	case int, int64, float64, bool:
		return fmt.Sprint(v)
	}
	return ""
}
func Strings(v any) []string {
	out := []string{}
	switch v := v.(type) {
	case []any:
		for _, x := range v {
			if s := scalar(x); s != "" {
				out = append(out, s)
			}
		}
	case []string:
		for _, s := range v {
			if strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	default:
		s := strings.Trim(strings.TrimSpace(scalar(v)), "[]")
		if s != "" {
			for _, x := range strings.Split(s, ",") {
				if x = strings.TrimSpace(x); x != "" {
					out = append(out, x)
				}
			}
		}
	}
	return out
}
func boolean(v any) *bool {
	s := strings.ToLower(scalar(v))
	b := true
	if s == "false" || s == "no" || s == "0" {
		b = false
	} else if s != "true" && s != "yes" && s != "1" {
		return nil
	}
	return &b
}
func choice(v any, fallback string, allowed ...string) string {
	s := scalar(v)
	for _, a := range allowed {
		if s == a {
			return s
		}
	}
	return fallback
}
func Validate(skills []Skill) Report {
	r := Report{Valid: true, Errors: []Issue{}, Warnings: []Issue{}}
	counts := map[string]int{}
	for _, s := range skills {
		counts[s.Name]++
	}
	for _, s := range skills {
		add := func(msg string) { r.Errors = append(r.Errors, Issue{s.Path, msg}) }
		if !slug.MatchString(s.Name) {
			add("name must be lowercase ASCII slug (letters, digits, single hyphens)")
		}
		if len(s.Name) > 64 {
			add("name must be 64 characters or less")
		}
		if reserved[s.Name] {
			add("name is reserved by the MyContextProtocol runtime")
		}
		if s.Frontmatter {
			if path.Base(path.Dir(s.Path)) != s.Name {
				add("name must exactly match the parent skill directory")
			}
			if s.Description == "" {
				add("description cannot be empty")
			} else if utf8.RuneCountInString(s.Description) > 1024 {
				add("description must be 1024 characters or less")
			}
		} else {
			r.Warnings = append(r.Warnings, Issue{s.Path, "No YAML front matter; skill name was inferred from the parent directory."})
		}
		if len(s.Body) > MaxSkillBytes {
			add("SKILL.md body exceeds size limit")
		}
		if counts[s.Name] > 1 {
			add("duplicate skill id " + s.Name + " in this release")
		}
	}
	r.Valid = len(r.Errors) == 0
	return r
}

func Policies(root string) (map[string]Policy, error) {
	filename := filepath.Join(root, ".mycontext", "skills.yaml")
	info, err := os.Lstat(filename)
	if os.IsNotExist(err) {
		return map[string]Policy{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 256<<10 {
		return nil, errors.New("unsafe or oversized .mycontext/skills.yaml")
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	var document struct {
		Version int                       `yaml:"version"`
		Skills  map[string]map[string]any `yaml:"skills"`
	}
	if err = decodeYAML(data, &document); err != nil {
		return nil, err
	}
	if document.Version != 1 || document.Skills == nil {
		return nil, errors.New("skills policy requires version 1 and skills mapping")
	}
	policies := map[string]Policy{}
	for id, row := range document.Skills {
		if !slug.MatchString(id) || len(id) > 64 || reserved[id] {
			return nil, errors.New("invalid source policy skill id")
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		var policy Policy
		if err = json.Unmarshal(encoded, &policy); err != nil || policy.Metadata == nil {
			return nil, errors.New("source policy metadata must be a mapping")
		}
		if err = validatePatch(policy.Metadata); err != nil {
			return nil, err
		}
		policy.Raw = string(encoded)
		policies[id] = policy
	}
	return policies, nil
}
func validatePatch(p map[string]any) error {
	enums := map[string][]string{"exposure": {"tool", "resource", "prompt"}, "kind": {"operating", "task", "tool-use", "reference"}, "scope": {"global", "organization", "workspace", "repository", "task"}, "enforcement": {"advisory", "required"}}
	for key, values := range enums {
		if v, ok := p[key]; ok && v != nil {
			if choice(v, "", values...) == "" {
				return fmt.Errorf("invalid metadata.%s", key)
			}
		}
	}
	if v, ok := p["priority"]; ok && v != nil {
		n, err := strconv.Atoi(scalar(v))
		if err != nil || n < 0 || n > 100 {
			return errors.New("metadata.priority must be between 0 and 100")
		}
	}
	if raw, ok := p["activation"]; ok && raw != nil {
		a, ok := raw.(map[string]any)
		if !ok || choice(a["mode"], "", "always", "intent", "event", "explicit") == "" {
			return errors.New("invalid metadata.activation")
		}
		for _, key := range []string{"intents", "events", "tags", "examples"} {
			if v, ok := a[key]; ok {
				list, ok := v.([]any)
				if !ok {
					return errors.New("activation lists must be arrays")
				}
				for _, s := range list {
					if _, ok := s.(string); !ok {
						return errors.New("activation lists must contain strings")
					}
				}
			}
		}
	}
	for _, key := range []string{"version", "lifecycle"} {
		if v, ok := p[key]; ok && v != nil {
			if _, ok := v.(string); !ok {
				return fmt.Errorf("metadata.%s must be a string", key)
			}
		}
	}
	for _, key := range []string{"avoid_when", "conflicts_with"} {
		if raw, ok := p[key]; ok && raw != nil {
			list, ok := raw.([]any)
			if !ok {
				return fmt.Errorf("metadata.%s must be an array", key)
			}
			for _, v := range list {
				if _, ok := v.(string); !ok {
					return fmt.Errorf("metadata.%s must contain strings", key)
				}
			}
		}
	}
	if raw, ok := p["requires"]; ok && raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return errors.New("metadata.requires must be an array")
		}
		for _, raw := range list {
			row, ok := raw.(map[string]any)
			if !ok {
				return errors.New("invalid metadata.requires entry")
			}
			if _, ok := row["capability"].(string); !ok {
				return errors.New("requirement capability must be a string")
			}
			if _, ok := row["required"].(bool); !ok {
				return errors.New("requirement required must be boolean")
			}
			fallback := row["on_missing"]
			if fallback == nil {
				fallback = row["onMissing"]
			}
			if choice(fallback, "", "fail_activation", "warn", "return_draft", "request_provider_selection", "continue_without_action") == "" {
				return errors.New("invalid requirement on_missing")
			}
		}
	}

	return nil
}
func Compile(s Skill, repository, revision string, policy *Policy) (Compiled, error) {
	return CompileWithOverride(s, repository, revision, policy, nil)
}

func CompileWithOverride(s Skill, repository, revision string, policy *Policy, override map[string]any) (Compiled, error) {
	c := Compiled{Skill: s, Clarifications: []map[string]any{}}
	effective := map[string]any{}
	for k, v := range s.Metadata {
		effective[k] = v
	}
	if policy != nil {
		c.SourcePolicyJSON = policy.Raw
		c.SourcePolicyChecksum = policy.BaseChecksum
		c.SourcePolicyStale = policy.BaseChecksum != "" && policy.BaseChecksum != s.Hash
		if !c.SourcePolicyStale {
			for k, v := range policy.Metadata {
				if v != nil {
					effective[k] = patchValue(k, v)
				}
			}
		}
	}
	if override != nil {
		if err := validatePatch(override); err != nil {
			return c, err
		}
		for k, v := range override {
			if v != nil {
				effective[k] = patchValue(k, v)
			}
		}
	}

	exposure := strings.ToLower(scalar(effective["exposure"]))
	if exposure == "" {
		exposure = strings.ToLower(scalar(s.Metadata["expose_as"]))
		if exposure == "guidance" {
			exposure = "prompt"
		}
		if exposure == "" {
			exposure = "resource"
		} else if choice(exposure, "", "tool", "resource", "prompt") == "" {
			exposure = "tool"
		}
	}
	c.Exposure = exposure
	c.Risk = choice(strings.ToLower(scalar(s.Metadata["risk_level"])), "low", "low", "medium", "high")
	c.SideEffects = choice(strings.ToLower(scalar(s.Metadata["side_effects"])), "none", "none", "read", "mutating", "destructive")
	if b := boolean(s.Metadata["repo_specific"]); b != nil {
		c.RepoSpecific = *b
	}
	c.Summary = prefix(s.Description, 2048)
	if c.Summary == "" {
		c.Summary = prefix(s.Body, 200)
	}
	c.UseWhen = Strings(s.Metadata["use_when"])
	// Portable activation intents are canonical runtime routing, distinct from
	// resource hints. Only source policy/dashboard activation overrides replace
	// the authored use_when hints in the Swift MCP compiler.
	var routingActivation map[string]any
	if policy != nil && !c.SourcePolicyStale {
		routingActivation, _ = policy.Metadata["activation"].(map[string]any)
	}
	if a, ok := override["activation"].(map[string]any); ok {
		routingActivation = a
	}
	if routingActivation != nil {
		c.UseWhen = Strings(routingActivation["intents"])
	}

	c.AvoidWhen = Strings(effective["avoid_when"])
	c.FailureModes = Strings(s.Metadata["failure_modes"])
	c.InvokeFirst = boolean(s.Metadata["invoke_first"])
	c.Status = "ready"
	if s.Description == "" {
		c.Status = "not_publishable"
	} else if c.Risk == "high" {
		c.Status = "needs_review"
	}
	if !s.Frontmatter || strings.TrimSpace(s.Body) == "" || (exposure == "resource" && len(c.UseWhen)+len(c.AvoidWhen)+len(c.FailureModes) == 0) {
		if c.Status != "not_publishable" {
			c.Status = "needs_review"
		}
	}
	switch exposure {
	case "resource":
		c.Schema = map[string]any{"uri": "ctx://skill/" + s.Name, "mimeType": "text/markdown"}
		for key, values := range map[string][]string{"use_when": c.UseWhen, "avoid_when": c.AvoidWhen, "failure_modes": c.FailureModes} {
			if len(values) > 0 {
				c.Schema[key] = values
			}
		}
		if c.InvokeFirst != nil && *c.InvokeFirst {
			c.Schema["invoke_first"] = true
		}
	case "prompt":
		c.Schema = map[string]any{"arguments": []any{}}
	case "tool":
		help := "Optional extra context or question for this skill."
		if c.Summary != "" {
			blurb := prefix(c.Summary, 480)
			if len([]rune(c.Summary)) > 480 {
				blurb += "…"
			}
			help = "Optional extra context. Skill summary: " + blurb
		}
		c.Schema = map[string]any{"type": "object", "properties": map[string]any{"detail": map[string]any{"type": "string", "description": help}}, "additionalProperties": false}
	default:
		return c, errors.New("invalid exposure")
	}
	missing := []string{}
	warnings := []string{}
	if !s.Frontmatter {
		for _, key := range []string{"kind", "scope", "activation", "enforcement", "version"} {
			if effective[key] == nil {
				missing = append(missing, key)
			}
		}
		warnings = append(warnings, "No YAML front matter; conservative runtime defaults applied.")
	}
	for _, key := range missing {
		c.Clarifications = append(c.Clarifications, map[string]any{"field": key, "question": "Configure " + key + " for this skill.", "options": []string{}, "required": true})
	}
	activation := map[string]any{"mode": "explicit", "intents": Strings(s.Metadata["use_when"]), "events": []string{}, "tags": []string{}, "examples": []string{}}
	if s.Frontmatter {
		activation["mode"] = "intent"
	}
	if a, ok := effective["activation"].(map[string]any); ok && choice(a["mode"], "", "always", "intent", "event", "explicit") != "" {
		activation["mode"] = scalar(a["mode"])
		for _, key := range []string{"intents", "events", "tags", "examples"} {
			if a[key] != nil {
				activation[key] = Strings(a[key])
			}
		}
	}
	priority := 50
	if n, err := strconv.Atoi(scalar(effective["priority"])); err == nil {
		priority = n
	}
	if priority < 0 {
		priority = 0
	}
	if priority > 100 {
		priority = 100
	}
	version := scalar(effective["version"])
	if version == "" {
		rev := strings.TrimSpace(revision)
		if rev == "" {
			rev = "unversioned"
		}
		version = rev + "+" + prefix(s.Hash, 12)
	}
	requirements := []map[string]any{}
	if list, ok := effective["requires"].([]any); ok {
		for _, raw := range list {
			row, ok := raw.(map[string]any)
			if !ok || scalar(row["capability"]) == "" {
				continue
			}
			required := true
			if b := boolean(row["required"]); b != nil {
				required = *b
			}
			fallback := "warn"
			if required {
				fallback = "fail_activation"
			}
			fallback = choice(row["on_missing"], fallback, "fail_activation", "warn", "return_draft", "request_provider_selection", "continue_without_action")
			requirements = append(requirements, map[string]any{"capability": scalar(row["capability"]), "required": required, "on_missing": fallback})
		}
	}
	c.Canonical = map[string]any{"schemaVersion": 1, "id": s.Name, "name": s.Name, "description": s.Description, "kind": choice(effective["kind"], "task", "operating", "task", "tool-use", "reference"), "scope": choice(effective["scope"], "task", "global", "organization", "workspace", "repository", "task"), "activation": activation, "enforcement": choice(effective["enforcement"], "advisory", "advisory", "required"), "priority": priority, "requires": requirements, "conflictsWith": Strings(effective["conflictsWith"]), "instructions": s.Body, "source": map[string]any{"repository": repository, "path": s.Path, "revision": revision, "checksum": s.Hash}, "version": version, "validation": map[string]any{"clarificationRequired": len(missing) > 0, "missingFields": missing, "warnings": warnings}}
	if s.Description == "" {
		c.Canonical["description"] = prefix(s.Body, 200)
	}
	if effective["conflicts_with"] != nil {
		c.Canonical["conflictsWith"] = Strings(effective["conflicts_with"])
	}
	if len(c.AvoidWhen) > 0 {
		c.Canonical["avoidWhen"] = c.AvoidWhen
	}
	if lifecycle := scalar(effective["lifecycle"]); lifecycle != "" {
		c.Canonical["lifecycle"] = lifecycle
	}
	if s.Frontmatter {
		raw, err := json.Marshal(s.Metadata)
		if err != nil {
			return c, err
		}
		c.Canonical["standardFrontmatterJson"] = string(raw)
	}
	return c, nil
}
func prefix(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func patchValue(key string, v any) any {
	if key != "activation" {
		return v
	}
	original, ok := v.(map[string]any)
	if !ok {
		return v
	}
	copy := map[string]any{}
	for k, v := range original {
		copy[k] = v
	}
	for _, key := range []string{"intents", "events", "tags", "examples"} {
		if _, ok := copy[key]; !ok {
			copy[key] = []any{}
		}
	}
	return copy
}
