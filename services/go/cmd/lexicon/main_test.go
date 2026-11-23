package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/lexiconpublish"
)

func TestDryRunNeedsNoCredentialsAndCannotPublishAccidentally(t *testing.T) {
	directory := t.TempDir()
	schema := `{"lexicon":1,"id":"dev.mycontextprotocol.test.record","defs":{"main":{"type":"record","key":"any","record":{"type":"object","properties":{"value":{"type":"string"}}}}}}`
	if err := os.WriteFile(filepath.Join(directory, "dev.mycontextprotocol.test.record.json"), []byte(schema), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"TURSO_DATABASE_URL", "TURSO_AUTH_TOKEN", "ATPROTO_ENABLED", "ATPROTO_OAUTH_PRIVATE_KEY"} {
		t.Setenv(name, "")
	}
	args := []string{"--directory", directory, "--expected-authority", lexiconpublish.Authority}
	var output bytes.Buffer
	if err := run(args, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"status": "dry-run"`) || !strings.Contains(output.String(), "_lexicon.test.mycontextprotocol.dev") || strings.Contains(output.String(), "payload_encrypted") {
		t.Fatal(output.String())
	}
	if err := run(append(args, "--publish"), &bytes.Buffer{}); err == nil {
		t.Fatal("publication without explicit account accepted")
	}
	if err := run(append(args, "--publish", "--account", "account", "--dry-run"), &bytes.Buffer{}); err == nil {
		t.Fatal("contradictory dry run accepted")
	}
	if err := run([]string{"--directory", directory}, &bytes.Buffer{}); err == nil {
		t.Fatal("implicit authority accepted")
	}
}
