package reposync

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type item struct {
	name, body string
	kind       byte
	link       string
}

func fixture(t *testing.T, items ...item) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, i := range items {
		kind := i.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		h := &tar.Header{Name: i.name, Mode: 0600, Typeflag: kind, Linkname: i.link}
		if kind == tar.TypeReg {
			h.Size = int64(len(i.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if kind == tar.TypeReg {
			if _, err := tw.Write([]byte(i.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body []byte) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), ContentLength: -1}
}

func TestArchiveURL(t *testing.T) {
	got, err := ArchiveURL("example", "skills.repo", "feature/nested")
	if err != nil || got != "https://api.github.com/repos/example/skills.repo/tarball/feature%2Fnested" {
		t.Fatalf("%s %v", got, err)
	}
	for _, v := range [][3]string{{"../bad", "repo", "main"}, {"owner", "repo?x=y", "main"}, {"owner", "repo", "../secret"}, {"owner", "repo", "x\\y"}, {"owner", "repo", "main\n"}, {"owner", "repo", "/main"}, {"owner", "repo", "main/"}, {"owner", "..", "main"}, {" owner", "repo", "main"}, {"owner", "repo", strings.Repeat("a", 256)}} {
		if _, err := ArchiveURL(v[0], v[1], v[2]); !errors.Is(err, ErrInvalidRepository) {
			t.Errorf("accepted %q", v)
		}
	}
}

func TestFetchAuthenticationFallback(t *testing.T) {
	archive := fixture(t, item{name: "owner-repo-0123456789abcdef0123456789abcdef01234567/skills/foo/SKILL.md", body: "hello"})
	for _, tc := range []struct {
		name                  string
		statuses              []int
		token                 string
		wantStatus, wantCalls int
	}{{"expired public", []int{401, 200}, "expired", 0, 2}, {"expired private", []int{401, 404}, "expired", 401, 2}, {"valid", []int{200}, "valid", 0, 1}, {"forbidden", []int{403}, "valid", 403, 1}, {"anonymous", []int{401}, "", 401, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			dir := t.TempDir()
			client := &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
				if _, ok := r.Context().Deadline(); !ok {
					t.Error("no deadline")
				}
				want := ""
				if calls == 0 && tc.token != "" {
					want = "Bearer " + tc.token
				}
				if got := r.Header.Get("Authorization"); got != want {
					t.Errorf("auth %q want %q", got, want)
				}
				status := tc.statuses[calls]
				calls++
				return response(status, archive), nil
			})}
			out, err := (Fetcher{Client: client, TempDir: dir}).Fetch(context.Background(), "owner", "repo", "main", tc.token)
			if calls != tc.wantCalls {
				t.Fatalf("calls %d", calls)
			}
			if tc.wantStatus != 0 {
				var status *StatusError
				if !errors.As(err, &status) || status.Status != tc.wantStatus {
					t.Fatalf("error %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer out.Cleanup()
			if out.ResolvedCommitSHA != "0123456789abcdef0123456789abcdef01234567" {
				t.Errorf("SHA %s", out.ResolvedCommitSHA)
			}
			files, err := FindSkillFiles(context.Background(), out.RepositoryRoot, 0)
			if err != nil || len(files) != 1 {
				t.Fatalf("%v %v", files, err)
			}
			data, err := os.ReadFile(files[0])
			if err != nil || string(data) != "hello" {
				t.Fatalf("%s %v", data, err)
			}
		})
	}
}

func TestRedirectPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		allowed      bool
	}{{"codeload", "https://codeload.github.com/owner/repo", true}, {"api", "https://api.github.com/final", true}, {"external", "https://attacker.example/repo", false}, {"downgrade", "http://codeload.github.com/repo", false}, {"port", "https://codeload.github.com:444/repo", false}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					res := response(302, nil)
					res.Header.Set("Location", tc.target)
					return res, nil
				}
				if r.Header.Get("Authorization") != "Bearer secret" {
					t.Error("private repo token not forwarded to allowed host")
				}
				return response(200, []byte("archive")), nil
			})}
			_, err := (Fetcher{Client: client}).download(context.Background(), "https://api.github.com/first", "secret", 100)
			if tc.allowed && err != nil {
				t.Fatal(err)
			}
			if !tc.allowed && (err == nil || calls != 1) {
				t.Fatalf("unsafe redirect followed: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestDownloadLimitsAndDeadline(t *testing.T) {
	client := &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) { return response(200, []byte("123456")), nil })}
	_, err := (Fetcher{Client: client}).download(context.Background(), "https://api.github.com/archive", "", 5)
	if !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatal(err)
	}
	client.Transport = transport(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	_, err = (Fetcher{Client: client, Limits: Limits{FetchTimeout: time.Millisecond}}).Fetch(context.Background(), "owner", "repo", "main", "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestExtractRejectsUnsafeEntriesAndCleansUp(t *testing.T) {
	for _, tc := range []struct {
		name   string
		items  []item
		limits Limits
		want   error
	}{
		{"parent", []item{{name: "../escape", body: "bad"}}, Limits{}, ErrUnsafeEntry},
		{"embedded parent", []item{{name: "safe/../escape", body: "bad"}}, Limits{}, ErrUnsafeEntry},
		{"absolute", []item{{name: "/escape", body: "bad"}}, Limits{}, ErrUnsafeEntry},
		{"windows", []item{{name: "safe\\escape", body: "bad"}}, Limits{}, ErrUnsafeEntry},
		{"symlink", []item{{name: "root/link", kind: tar.TypeSymlink, link: "../../escape"}}, Limits{}, ErrUnsafeEntry},
		{"internal symlink", []item{{name: "root/link", kind: tar.TypeSymlink, link: "file"}}, Limits{}, ErrUnsafeEntry},
		{"hardlink", []item{{name: "root/link", kind: tar.TypeLink, link: "root/file"}}, Limits{}, ErrUnsafeEntry},
		{"device", []item{{name: "root/device", kind: tar.TypeChar}}, Limits{}, ErrUnsafeEntry},
		{"duplicate", []item{{name: "root/file", body: "a"}, {name: "root/file", body: "b"}}, Limits{}, ErrUnsafeEntry},
		{"expanded limit", []item{{name: "root/file", body: "123456"}}, Limits{ExpandedBytes: 5}, ErrExpandedTooLarge},
		{"entry limit includes hidden", []item{{name: "root/.hidden", body: "a"}, {name: "root/other", body: "b"}}, Limits{Entries: 1}, ErrTooManyEntries},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			_, err := (Fetcher{TempDir: dir, Limits: tc.limits}).Extract(context.Background(), bytes.NewReader(fixture(t, tc.items...)))
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			entries, e := os.ReadDir(dir)
			if e != nil || len(entries) != 0 {
				t.Fatalf("temporary data leaked: %v %v", entries, e)
			}
		})
	}
}

func TestExtractRejectsTruncatedGzip(t *testing.T) {
	data := fixture(t, item{name: "root/SKILL.md", body: "hello"})
	for _, data := range [][]byte{[]byte("not gzip"), data[:len(data)-6]} {
		dir := t.TempDir()
		if _, err := (Fetcher{TempDir: dir}).Extract(context.Background(), bytes.NewReader(data)); err == nil {
			t.Fatal("accepted corrupt archive")
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Fatal("leaked temporary extraction")
		}
	}
}
func TestExtractCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := t.TempDir()
	_, err := (Fetcher{TempDir: dir}).Extract(ctx, bytes.NewReader(fixture(t, item{name: "root/file"})))
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("leaked temporary extraction")
	}
}

func TestFindSkillsRecursiveHiddenAndLimit(t *testing.T) {
	dir := t.TempDir()
	out, err := (Fetcher{TempDir: dir}).Extract(context.Background(), bytes.NewReader(fixture(t, item{name: "root/a/SKILL.md", body: "a"}, item{name: "root/b/deep/SKILL.md", body: "b"}, item{name: "root/.hidden/SKILL.md", body: "hidden"}, item{name: "root/c/skill.md", body: "wrong case"})))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Cleanup()
	if err = os.Symlink(filepath.Join(out.RepositoryRoot, "a", "SKILL.md"), filepath.Join(out.RepositoryRoot, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	files, err := FindSkillFiles(context.Background(), out.RepositoryRoot, 5)
	if err != nil || len(files) != 2 {
		t.Fatalf("%v %v", files, err)
	}
	files, err = FindSkillFiles(context.Background(), out.RepositoryRoot, 1)
	if err != nil || len(files) != 1 {
		t.Fatalf("%v %v", files, err)
	}
}
func TestMultipleRepositoryRoots(t *testing.T) {
	out, err := (Fetcher{TempDir: t.TempDir()}).Extract(context.Background(), bytes.NewReader(fixture(t, item{name: "one/SKILL.md", body: "1"}, item{name: "two/SKILL.md", body: "2"})))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Cleanup()
	if out.RepositoryRoot != out.ExtractPath || out.ResolvedCommitSHA != "" {
		t.Fatalf("unexpected root %+v", out)
	}
}

func TestExtractBoundsConcatenatedGzipPadding(t *testing.T) {
	data := fixture(t, item{name: "root/SKILL.md", body: "a"})
	var padding bytes.Buffer
	gz := gzip.NewWriter(&padding)
	if _, err := gz.Write(make([]byte, 2<<20)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	data = append(data, padding.Bytes()...)
	dir := t.TempDir()
	_, err := (Fetcher{TempDir: dir, Limits: Limits{ExpandedBytes: 1, Entries: 1}}).Extract(context.Background(), bytes.NewReader(data))
	if !errors.Is(err, ErrExpandedTooLarge) {
		t.Fatalf("accepted gzip padding bomb: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("temporary files leaked")
	}
}

func TestDeadlineDuringExtraction(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err := (Fetcher{TempDir: t.TempDir()}).Extract(ctx, bytes.NewReader(fixture(t, item{name: "root/SKILL.md", body: "a"})))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
