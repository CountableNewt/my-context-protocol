package compiler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStandardSkillCompilation(t *testing.T) {
	source := []byte("---\nname: useful-skill\ndescription: >-\n  Helps when writing\n  useful code.\nuse_when:\n  - write code\nlicense: MIT\nmetadata:\n  author: Sam\nallowed-tools: Read Bash\n---\n# Instructions\nDo the work.\n")
	s, err := Parse("nested/useful-skill/SKILL.md", source)
	if err != nil {
		t.Fatal(err)
	}
	if s.Description != "Helps when writing useful code." {
		t.Fatalf("description %q", s.Description)
	}
	if !Validate([]Skill{s}).Valid {
		t.Fatal("standard skill invalid")
	}
	c, err := Compile(s, "owner/repo", "revision", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Exposure != "resource" || c.Status != "ready" || c.Schema["uri"] != "ctx://skill/useful-skill" {
		t.Fatalf("%+v", c)
	}
	if c.Canonical["version"] != "revision+"+s.Hash[:12] {
		t.Fatal(c.Canonical)
	}
	activation := c.Canonical["activation"].(map[string]any)
	if activation["mode"] != "intent" {
		t.Fatal(activation)
	}
	raw := c.Canonical["standardFrontmatterJson"].(string)
	var preserved map[string]any
	if json.Unmarshal([]byte(raw), &preserved) != nil || preserved["license"] != "MIT" || preserved["allowed-tools"] != "Read Bash" {
		t.Fatal(raw)
	}
}
func TestParseValidationFailures(t *testing.T) {
	for _, tc := range []struct{ name, source string }{{"empty", "  "}, {"unclosed", "---\nname: a"}, {"missing name", "---\ndescription: hi\n---\nbody"}, {"invalid yaml", "---\nname: [\n---\nbody"}, {"duplicate key", "---\nname: a\nname: b\n---\nbody"}} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse("a/SKILL.md", []byte(tc.source)); err == nil {
				t.Fatal("accepted invalid skill")
			}
		})
	}
	if _, err := Parse("a/SKILL.md", []byte{0xff}); err == nil {
		t.Fatal("accepted invalid UTF-8")
	}
	if _, err := Parse("a/SKILL.md", []byte(strings.Repeat("a", MaxSkillBytes+1))); err == nil {
		t.Fatal("accepted oversized skill")
	}
}
func TestFrontmatterlessConservativeDefaults(t *testing.T) {
	s, err := Parse("legacy-skill/SKILL.md", []byte("# Legacy\nUseful instructions."))
	if err != nil {
		t.Fatal(err)
	}
	r := Validate([]Skill{s})
	if !r.Valid || len(r.Warnings) != 1 {
		t.Fatal(r)
	}
	c, err := Compile(s, "owner/repo", "sha", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Exposure != "resource" || c.Status != "not_publishable" || len(c.Clarifications) != 5 {
		t.Fatalf("%+v", c)
	}
	if c.Canonical["activation"].(map[string]any)["mode"] != "explicit" {
		t.Fatal(c.Canonical)
	}
}
func TestValidationNamesAndDuplicates(t *testing.T) {
	s, _ := Parse("a/SKILL.md", []byte("---\nname: wrong-name\ndescription: test\n---\nbody"))
	if Validate([]Skill{s}).Valid {
		t.Fatal("accepted folder mismatch")
	}
	s, _ = Parse("same/SKILL.md", []byte("---\nname: same\ndescription: test\n---\nbody"))
	r := Validate([]Skill{s, s})
	if r.Valid || len(r.Errors) != 2 {
		t.Fatal(r)
	}
}
func TestPolicyFreshAndStale(t *testing.T) {
	s, _ := Parse("a/SKILL.md", []byte("---\nname: a\ndescription: test\nuse_when: writing\n---\nbody"))
	p := Policy{BaseChecksum: s.Hash, Metadata: map[string]any{"exposure": "tool", "priority": 90, "scope": "repository"}, Raw: "{}"}
	c, err := Compile(s, "owner/repo", "sha", &p)
	if err != nil || c.Exposure != "tool" || c.Canonical["priority"] != 90 || c.Canonical["scope"] != "repository" {
		t.Fatalf("%+v %v", c, err)
	}
	p.BaseChecksum = "stale"
	c, err = Compile(s, "owner/repo", "sha", &p)
	if err != nil || !c.SourcePolicyStale || c.Exposure != "resource" || c.Canonical["scope"] != "task" {
		t.Fatalf("%+v %v", c, err)
	}
}
func TestSourcePolicies(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{{"valid", "version: 1\nskills:\n  a:\n    metadata:\n      exposure: resource\n      activation:\n        mode: intent\n        intents: [write code]\n", true}, {"wrong version", "version: 2\nskills: {}", false}, {"bad priority", "version: 1\nskills:\n  a:\n    metadata:\n      priority: 101", false}, {"bad activation", "version: 1\nskills:\n  a:\n    metadata:\n      activation:\n        mode: intent\n        events: hi", false}, {"missing metadata", "version: 1\nskills:\n  a:\n    priority: 50", false}} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			os.Mkdir(filepath.Join(root, ".mycontext"), 0700)
			os.WriteFile(filepath.Join(root, ".mycontext", "skills.yaml"), []byte(tc.body), 0600)
			_, err := Policies(root)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}
func TestPackageFilesBoundAndExcludeNestedSkills(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "nested"), 0700)
	os.MkdirAll(filepath.Join(root, ".hidden"), 0700)
	for p, s := range map[string]string{"SKILL.md": "root", "helper.py": "print(1)", "nested/SKILL.md": "nested", "nested/helper.py": "nested helper", ".hidden/secret.txt": "hidden"} {
		os.WriteFile(filepath.Join(root, p), []byte(s), 0600)
	}
	files, err := PackageFiles(context.Background(), root)
	if err != nil || len(files) != 1 || files[0].Path != "helper.py" || files[0].ContentType != "text/x-python" {
		t.Fatalf("%+v %v", files, err)
	}
	os.WriteFile(filepath.Join(root, "huge"), make([]byte, (256<<10)+1), 0600)
	if _, err = PackageFiles(context.Background(), root); err == nil {
		t.Fatal("accepted oversized ancillary file")
	}
}
func TestExplicitExposureAndHighRisk(t *testing.T) {
	for _, exposure := range []string{"tool", "prompt", "guidance", "resource"} {
		s, err := Parse("a/SKILL.md", []byte("---\nname: a\ndescription: hi\nexpose_as: "+exposure+"\nrisk_level: high\nuse_when: writing\n---\nbody"))
		if err != nil {
			t.Fatal(err)
		}
		c, err := Compile(s, "repo", "rev", nil)
		if err != nil || c.Status != "needs_review" {
			t.Fatalf("%+v %v", c, err)
		}
		want := exposure
		if exposure == "guidance" {
			want = "prompt"
		}
		if c.Exposure != want {
			t.Fatal(c.Exposure)
		}
	}
}

func TestBodyDiff(t *testing.T) {
	if BodyDiff("same\n", "same\n") != "" {
		t.Fatal("identical diff")
	}
	diff := BodyDiff("alpha\nbeta\ngamma\n", "alpha\nBETA\ngamma\n")
	if !strings.Contains(diff, "-beta\n+BETA\n") || !strings.Contains(diff, " alpha\n") || !strings.HasPrefix(diff, "--- prior release\n+++ new commit\n") {
		t.Fatal(diff)
	}
	if diff = BodyDiff("", "new\n"); !strings.Contains(diff, "+new\n") {
		t.Fatal(diff)
	}
	if diff = BodyDiff("old\n", ""); !strings.Contains(diff, "-old\n") {
		t.Fatal(diff)
	}
	diff = BodyDiff(strings.Repeat("old\n", 2501), strings.Repeat("new\n", 2501))
	if !strings.Contains(diff, "Large file:") || !strings.Contains(diff, "truncated") {
		t.Fatal("large diff was not bounded")
	}
	diff = BodyDiff(strings.Repeat("x", 70000), "new")
	if !strings.Contains(diff, "diff truncated") {
		t.Fatal("long-line diff was not capped")
	}
}

func TestRuntimeOverridePrecedenceAndActivationDefaults(t *testing.T) {
	s, _ := Parse("a/SKILL.md", []byte("---\nname: a\ndescription: useful\nuse_when: writing\n---\nbody"))
	policy := Policy{Metadata: map[string]any{"priority": 80, "scope": "repository"}}
	c, err := CompileWithOverride(s, "owner/repo", "sha", &policy, map[string]any{"priority": 90, "scope": "workspace", "activation": map[string]any{"mode": "event"}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Canonical["priority"] != 90 || c.Canonical["scope"] != "workspace" || len(c.UseWhen) != 0 {
		t.Fatalf("%+v", c)
	}
	activation := c.Canonical["activation"].(map[string]any)
	if len(activation["intents"].([]string)) != 0 || activation["mode"] != "event" {
		t.Fatal(activation)
	}
}

func TestRawActivationIntentsDoNotManufactureResourceHints(t *testing.T) {
	s, err := Parse("a/SKILL.md", []byte("---\nname: a\ndescription: useful\nactivation:\n  mode: event\n  intents: [write code]\n---\nbody"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := Compile(s, "owner/repo", "sha", nil)
	if err != nil || len(c.UseWhen) != 0 || c.Status != "needs_review" {
		t.Fatalf("%+v %v", c, err)
	}
	if len(c.Canonical["activation"].(map[string]any)["intents"].([]string)) != 1 {
		t.Fatal("canonical intents were lost")
	}
}
func TestCRFrontmatterAndNullPolicyFields(t *testing.T) {
	s, err := Parse("a/SKILL.md", []byte("---\rname: a\rdescription: useful\rkind: operating\ruse_when: writing\ractivation:\r  mode: event\r---\rbody"))
	if err != nil || !s.Frontmatter || s.Name != "a" || s.Body != "body" {
		t.Fatalf("%+v %v", s, err)
	}
	policy := Policy{Metadata: map[string]any{"kind": nil, "activation": nil, "priority": nil}}
	if err = validatePatch(policy.Metadata); err != nil {
		t.Fatal(err)
	}
	c, err := Compile(s, "owner/repo", "sha", &policy)
	if err != nil || c.Canonical["kind"] != "operating" || c.Canonical["activation"].(map[string]any)["mode"] != "event" || len(c.UseWhen) != 1 {
		t.Fatalf("%+v %v", c, err)
	}
}
