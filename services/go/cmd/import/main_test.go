package main

import (
	"os"
	"strings"
	"testing"
)

func TestExpectedHostMatchesOnlyExactValidatedDestination(t *testing.T) {
	for _, tc := range []struct{ host, url string }{{"db-owner.turso.io", "libsql://db-owner.turso.io"}, {"DB-OWNER.TURSO.IO", "https://db-owner.turso.io/"}, {"127.0.0.1", "http://127.0.0.1:8080"}} {
		if e := validateExpectedHost(tc.host, tc.url); e != nil {
			t.Fatal("valid exact target rejected")
		}
	}
	for _, host := range []string{"", "*.turso.io", "https://db-owner.turso.io", "db-owner.turso.io/path", "db-owner.turso.io?x=1", "user@db-owner.turso.io", "db-owner.turso.io:443", "db-owner.turso.io#x", " db-owner.turso.io", "db-owner.turso.io.", "db..turso.io", "-db.turso.io"} {
		if validateExpectedHost(host, "libsql://db-owner.turso.io") == nil {
			t.Fatal("invalid expected hostname accepted")
		}
	}
	for _, target := range []string{"libsql://other-owner.turso.io", "libsql://db-owner.turso.io.evil.example", "https://user:private@db-owner.turso.io", "https://db-owner.turso.io/path", "https://db-owner.turso.io?secret=private", "https://db-owner.turso.io#x", "http://db-owner.turso.io", "file:///db-owner.turso.io", "not a URL"} {
		if validateExpectedHost("db-owner.turso.io", target) == nil {
			t.Fatal("mismatched or unsafe destination accepted")
		}
	}
}
func TestLoadAndVerifyRequireHostBeforeCredentialsOrFileAccess(t *testing.T) {
	t.Setenv("TURSO_DATABASE_URL", "libsql://db-owner.turso.io")
	t.Setenv("TURSO_AUTH_TOKEN", "")
	for _, mode := range []string{"load", "verify"} {
		e := run([]string{mode, "--snapshot", "/nonexistent/local-artifact"})
		if e == nil || !strings.Contains(e.Error(), "expected-host") {
			t.Fatal("host guard did not run first")
		}
		e = run([]string{mode, "--snapshot", "/nonexistent/local-artifact", "--expected-host", "other.turso.io"})
		if e == nil || !strings.Contains(e.Error(), "does not match") {
			t.Fatal("wrong target reached credentials/file access")
		}
	}
}

func TestRemoteExportRequiresIndependentHostAndPausedWriters(t *testing.T) {
	t.Setenv("TURSO_DATABASE_URL", "libsql://db-owner.turso.io")
	t.Setenv("TURSO_AUTH_TOKEN", "")
	for _, tc := range []struct {
		args    []string
		message string
	}{
		{[]string{"export-remote", "--output", "/nonexistent/output"}, "writers-paused"},
		{[]string{"export-remote", "--output", "/nonexistent/output", "--writers-paused"}, "expected-host"},
		{[]string{"export-remote", "--output", "/nonexistent/output", "--writers-paused", "--expected-host", "wrong.turso.io"}, "does not match"},
	} {
		e := run(tc.args)
		if e == nil || !strings.Contains(e.Error(), tc.message) {
			t.Fatal("remote export guard did not run before credentials/network access")
		}
	}
}
func TestRestoreLocalRequiresNewOutputAndNoCredentials(t *testing.T) {
	t.Setenv("TURSO_DATABASE_URL", "not a URL")
	t.Setenv("TURSO_AUTH_TOKEN", "")
	if e := run([]string{"restore-local", "--snapshot", "/nonexistent/artifact"}); e == nil || !strings.Contains(e.Error(), "--output") {
		t.Fatal("missing local output accepted")
	}
	output := t.TempDir() + "/exists.sqlite"
	if e := os.WriteFile(output, []byte("existing database"), 0600); e != nil {
		t.Fatal(e)
	}
	e := run([]string{"restore-local", "--snapshot", "/nonexistent/artifact", "--output", output})
	if e == nil || !strings.Contains(e.Error(), "already exist") {
		t.Fatal("existing output reached credentials or source read")
	}
}
