package runtime

import (
	"encoding/json"
	"strings"
	"testing"
)

func doc(id, mode, kind, scope string) *Document {
	return &Document{SchemaVersion: 1, ID: id, Name: id, Description: "deployment testing tools", Kind: kind, Scope: scope, Activation: Activation{Mode: mode, Intents: []string{"deployment"}, Events: []string{"test_failed"}, Tags: []string{}, Examples: []string{}}, Enforcement: "advisory", Requires: []Requirement{}, ConflictsWith: []string{}, Instructions: "Apply this guidance.", Source: Source{Path: id + "/SKILL.md", Checksum: "abc"}, Version: "v1", Validation: Validation{MissingFields: []string{}, Warnings: []string{}}}
}
func snapshot(documents ...*Document) Snapshot {
	s := Snapshot{ProjectID: "project", ReleaseID: "release", Skills: []Skill{}, Assignments: []Assignment{}}
	for i, d := range documents {
		s.Skills = append(s.Skills, Skill{ID: string(rune('a' + i)), ReleaseID: "release", SkillID: d.ID, Version: d.Version, Document: d})
	}
	return s
}
func TestCanonicalScopeAssignmentAndAvoidRules(t *testing.T) {
	global := doc("global", "always", "operating", "global")
	repository := doc("repository", "always", "task", "repository")
	blocked := doc("blocked", "intent", "task", "global")
	blocked.AvoidWhen = []string{"deployment testing"}
	clarification := doc("clarification", "always", "operating", "global")
	clarification.Validation.ClarificationRequired = true
	s := snapshot(global, repository, blocked, clarification)
	s.Assignments = []Assignment{{SkillID: "repository", Scope: "repository", ActivationMode: "always", TargetType: "repository", TargetID: "owner/repo", Required: true, Priority: 900}}
	r, err := Resolve(s, ResolveInput{Request: "deployment testing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ActiveSkills) != 1 || r.ActiveSkills[0].ID != "global" {
		t.Fatalf("scope/avoid/clarification bypassed: %+v", r)
	}
	r, err = Resolve(s, ResolveInput{Request: "deployment testing", Context: Context{Repository: "owner/repo"}, CurrentSkillIDs: []string{"blocked"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ActiveSkills) != 3 || r.ActiveSkills[0].ID != "repository" || r.ActiveSkills[0].Enforcement != "required" {
		t.Fatalf("assignment/current priority failed: %+v", r.ActiveSkills)
	}
	// A legacy assignment with no target must never activate a policy outside context.
	s = snapshot(repository)
	s.Assignments = []Assignment{{SkillID: "repository", Scope: "repository", ActivationMode: "always", TargetType: "legacy_unscoped", TargetID: "legacy_unscoped", Required: true}}
	r, _ = Resolve(s, ResolveInput{Request: "deployment"})
	if len(r.ActiveSkills) != 0 {
		t.Fatal("unscoped legacy assignment activated")
	}
}
func TestRequiredCapabilitiesConflictsAndInlineLimit(t *testing.T) {
	a := doc("alpha", "always", "operating", "global")
	a.Enforcement = "required"
	a.Requires = []Requirement{{Capability: "issue.create", Required: true, OnMissing: "fail_activation"}}
	a.ConflictsWith = []string{"beta"}
	b := doc("beta", "always", "operating", "global")
	b.Instructions = strings.Repeat("x", InlineLimit+1)
	r, _ := Resolve(snapshot(a, b), ResolveInput{Request: "deployment"})
	if len(r.ActiveSkills) != 1 || r.ActiveSkills[0].ID != "beta" || len(r.MissingRequirements) != 1 || r.NextActions[0].Type != "read_skill" || r.ActiveSkills[0].Instructions != nil {
		t.Fatalf("capability/inline limits failed: %+v", r)
	}
	r, _ = Resolve(snapshot(a, b), ResolveInput{Request: "deployment", AvailableTools: []Tool{{Server: "linear", Name: "create_issue", Description: "Create issue"}}})
	if len(r.ActiveSkills) != 2 || r.ActiveSkills[0].ID != "alpha" || len(r.Conflicts) != 1 || r.CapabilityBindings[0].Missing {
		t.Fatalf("capability resolution failed: %+v", r)
	}
}
func TestEventsAreExactAndExplicitSkillsNeedActivation(t *testing.T) {
	a := doc("event", "event", "task", "global")
	b := doc("explicit", "explicit", "task", "global")
	r, _ := Resolve(snapshot(a, b), ResolveInput{Request: "deployment", Event: "TEST_FAILED"})
	if len(r.SuggestedTaskSkills) != 0 || r.EventCanonical == nil || *r.EventCanonical {
		t.Fatal("event matching not exact")
	}
	r, _ = Resolve(snapshot(a, b), ResolveInput{Request: "deployment", Event: "test_failed", CurrentSkillIDs: []string{"explicit"}})
	if len(r.SuggestedTaskSkills) != 1 || len(r.ActiveSkills) != 1 || !*r.EventCanonical {
		t.Fatal("event/current activation failed")
	}
}
func TestCanonicalDecoderRequiresValidationAndLegacyFallbackAlias(t *testing.T) {
	d := doc("policy", "always", "operating", "global")
	d.Requires = []Requirement{{Capability: "issue.create", Required: true, OnMissing: "return_draft"}}
	raw, _ := json.Marshal(d)
	parsed, err := ParseDocument(string(raw))
	if err != nil || parsed.Requires[0].OnMissing != "return_draft" {
		t.Fatalf("decode failed %s %v", raw, err)
	}
	legacy := strings.Replace(string(raw), "on_missing", "onMissing", 1)
	if _, err = ParseDocument(legacy); err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	_ = json.Unmarshal(raw, &object)
	delete(object, "validation")
	raw, _ = json.Marshal(object)
	if _, err = ParseDocument(string(raw)); err == nil {
		t.Fatal("missing validation defaults enabled policy")
	}
}
func TestSafePackagePathsAndVersionURIs(t *testing.T) {
	for _, path := range []string{"../secret", "a/../secret", "/root", "~/.ssh/key", "a\\b", "a//b", "a/%2e%2e/b", "a/%252e%252e/b", "a/./b", "a/\x00b"} {
		if _, err := SafePath(path); err == nil {
			t.Errorf("unsafe path accepted %q", path)
		}
	}
	uri := ResourceURI("skill name", "references/file.md", "v1+build&meta")
	ref, err := ParseResourceURI(uri)
	if err != nil || ref.SkillID != "skill name" || ref.Path != "references/file.md" || ref.Version != "v1+build&meta" {
		t.Fatalf("URI roundtrip: %s %+v %v", uri, ref, err)
	}
	for _, uri := range []string{"ctx://skill/a/file/../secret", "ctx://skill/a/file/%2fsecret", "ctx://skill/a?version=v1&extra=1", "ctx://skill/a#fragment", "ctx://skill/a/file/%252e%252e/secret"} {
		if _, err := ParseResourceURI(uri); err == nil {
			t.Errorf("unsafe resource URI accepted %q", uri)
		}
	}
}
func TestResolveRejectsMalformedIdentityFields(t *testing.T) {
	for _, raw := range []string{`{"request":"task","context":null}`, `{"request":"task","context":{"repository":17}}`, `{"request":"task","available_tools":null}`, `{"request":"task","current_skill_ids":null}`, `{"request":"task","available_tools":[{"server":"a"}]}`} {
		var args map[string]json.RawMessage
		_ = json.Unmarshal([]byte(raw), &args)
		if _, err := DecodeResolve(args); err == nil {
			t.Errorf("accepted malformed input %s", raw)
		}
	}
}
