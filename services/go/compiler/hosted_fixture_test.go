package compiler

import (
	"os"
	"strings"
	"testing"
)

func TestHostedSmokeFixtureCompilesAsAResource(t *testing.T) {
	const path = "testdata/hosted-smoke/SKILL.md"
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	skill, err := Parse(path, source)
	if err != nil || !Validate([]Skill{skill}).Valid {
		t.Fatal("hosted acceptance fixture must be publishable")
	}
	compiled, err := Compile(skill, "CountableNewt/my-context-protocol", "fixture", nil)
	if err != nil || compiled.Status != "ready" || compiled.Exposure != "resource" {
		t.Fatal("hosted acceptance fixture must compile to a ready resource")
	}
	if !strings.Contains(string(source), "MCP_HOSTED_SMOKE_V1") {
		t.Fatal("hosted acceptance marker is missing")
	}
}
