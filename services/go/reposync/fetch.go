// Package reposync downloads bounded GitHub repository snapshots without invoking
// a shell. Archives are extracted into a caller-owned temporary directory.
package reposync

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	ErrInvalidRepository = errors.New("invalid GitHub repository or ref")
	ErrArchiveTooLarge   = errors.New("repository archive exceeds download limit")
	ErrExpandedTooLarge  = errors.New("repository archive exceeds expanded limit")
	ErrTooManyEntries    = errors.New("repository archive exceeds entry limit")
	ErrUnsafeEntry       = errors.New("unsafe repository archive entry")
)

type StatusError struct{ Status int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("GitHub archive fetch failed: HTTP %d", e.Status)
}

type Limits struct {
	ArchiveBytes   int64
	ExpandedBytes  int64
	Entries        int
	SkillFiles     int
	FetchTimeout   time.Duration
	ExtractTimeout time.Duration
}

func DefaultLimits() Limits {
	return Limits{150 << 20, 500 << 20, 20000, 5000, 60 * time.Second, 30 * time.Second}
}
func (l Limits) defaults() Limits {
	d := DefaultLimits()
	if l.ArchiveBytes <= 0 {
		l.ArchiveBytes = d.ArchiveBytes
	}
	if l.ExpandedBytes <= 0 {
		l.ExpandedBytes = d.ExpandedBytes
	}
	if l.Entries <= 0 {
		l.Entries = d.Entries
	}
	if l.SkillFiles <= 0 {
		l.SkillFiles = d.SkillFiles
	}
	if l.FetchTimeout <= 0 {
		l.FetchTimeout = d.FetchTimeout
	}
	if l.ExtractTimeout <= 0 {
		l.ExtractTimeout = d.ExtractTimeout
	}
	return l
}

type Fetcher struct {
	// Client's transport may be injected for tests. Redirect policy is always enforced.
	Client  *http.Client
	Limits  Limits
	TempDir string
}
type Outcome struct{ ExtractPath, RepositoryRoot, TempRoot, ResolvedCommitSHA string }

func (o Outcome) Cleanup() error { return os.RemoveAll(o.TempRoot) }

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
var refPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
var shaPattern = regexp.MustCompile(`(?i)[a-f0-9]{40}$`)

func ArchiveURL(owner, repository, ref string) (string, error) {
	if owner != strings.TrimSpace(owner) || repository != strings.TrimSpace(repository) || ref != strings.TrimSpace(ref) || len(owner) == 0 || len(owner) > 100 || len(repository) == 0 || len(repository) > 100 || !repositoryPattern.MatchString(owner) || !repositoryPattern.MatchString(repository) || owner == "." || owner == ".." || repository == "." || repository == ".." || len(ref) == 0 || len(ref) > 255 || !refPattern.MatchString(ref) || strings.Contains(ref, "..") || strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") {
		return "", ErrInvalidRepository
	}
	return "https://api.github.com/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repository) + "/tarball/" + url.PathEscape(ref), nil
}

// Fetch retries an optional rejected credential once anonymously, allowing public
// repositories to sync after a GitHub token expires. Private repositories fail closed.
func (f Fetcher) Fetch(ctx context.Context, owner, repository, ref, token string) (Outcome, error) {
	endpoint, err := ArchiveURL(owner, repository, ref)
	if err != nil {
		return Outcome{}, err
	}
	limits := f.Limits.defaults()
	fetchCtx, cancel := context.WithTimeout(ctx, limits.FetchTimeout)
	defer cancel()
	data, err := f.download(fetchCtx, endpoint, token, limits.ArchiveBytes)
	if err != nil {
		return Outcome{}, err
	}
	extractCtx, stop := context.WithTimeout(ctx, limits.ExtractTimeout)
	defer stop()
	return f.Extract(extractCtx, bytes.NewReader(data))
}

func (f Fetcher) download(ctx context.Context, endpoint, token string, max int64) ([]byte, error) {
	client := http.Client{}
	if f.Client != nil {
		client = *f.Client
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many GitHub redirects")
		}
		if req.URL.Scheme != "https" || (req.URL.Host != "api.github.com" && req.URL.Host != "codeload.github.com") || req.URL.User != nil {
			return errors.New("untrusted GitHub archive redirect")
		}
		req.Header.Del("Authorization")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return nil
	}
	request := func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github.v3+json")
		req.Header.Set("User-Agent", "MyContextProtocol/1")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return client.Do(req)
	}
	response, err := request()
	if err != nil {
		return nil, err
	}
	retried := response.StatusCode == http.StatusUnauthorized && token != ""
	if retried {
		response.Body.Close()
		token = ""
		response, err = request()
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, &StatusError{401}
		}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		status := response.StatusCode
		if retried {
			status = 401
		}
		return nil, &StatusError{status}
	}
	if response.ContentLength > max {
		return nil, ErrArchiveTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(&contextReader{ctx, response.Body}, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, ErrArchiveTooLarge
	}
	return data, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// Extract rejects links and special files before writing, rather than trusting
// system tar and validating afterward. It never follows archive-created links.
func (f Fetcher) Extract(ctx context.Context, compressed io.Reader) (out Outcome, err error) {
	limits := f.Limits.defaults()
	ctx, cancel := context.WithTimeout(ctx, limits.ExtractTimeout)
	defer cancel()
	root, err := os.MkdirTemp(f.TempDir, "mycontext-repo-")
	if err != nil {
		return out, err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(root)
		}
	}()
	extracted := filepath.Join(root, "extracted")
	if err = os.Mkdir(extracted, 0700); err != nil {
		return out, err
	}
	// Bound compressed reads for direct extraction callers too.
	bounded := &io.LimitedReader{R: &contextReader{ctx, compressed}, N: limits.ArchiveBytes + 1}
	gz, err := gzip.NewReader(bounded)
	if err != nil {
		return out, err
	}
	defer gz.Close()
	// Bound tar metadata, padding and hidden entries as well as regular contents.
	streamLimit := limits.ExpandedBytes + int64(limits.Entries)*2048 + (1 << 20)
	expanded := &io.LimitedReader{R: &contextReader{ctx, gz}, N: streamLimit + 1}
	archive := tar.NewReader(expanded)
	var bytes int64
	entries := 0
	for {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		var h *tar.Header
		h, err = archive.Next()
		if err == io.EOF {
			err = nil
			break
		}
		if err != nil {
			return out, err
		}
		entries++
		if entries > limits.Entries {
			return out, ErrTooManyEntries
		}
		name := strings.TrimSuffix(h.Name, "/")
		clean := path.Clean(name)
		if name == "" || strings.Contains(name, "\\") || strings.ContainsRune(name, 0) || path.IsAbs(name) || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(name, ":") {
			return out, ErrUnsafeEntry
		}
		for _, part := range strings.Split(name, "/") {
			if part == ".." {
				return out, ErrUnsafeEntry
			}
		}
		if clean == "." {
			if h.Typeflag == tar.TypeDir {
				continue
			}
			return out, ErrUnsafeEntry
		}
		dest := filepath.Join(extracted, filepath.FromSlash(clean))
		relative, e := filepath.Rel(extracted, dest)
		if e != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return out, ErrUnsafeEntry
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err = os.MkdirAll(dest, 0700); err != nil {
				return out, err
			}
		case tar.TypeReg, tar.TypeRegA:
			if h.Size < 0 || h.Size > limits.ExpandedBytes-bytes {
				return out, ErrExpandedTooLarge
			}
			bytes += h.Size
			if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
				return out, err
			}
			var file *os.File
			file, err = os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return out, fmt.Errorf("%w: duplicate or conflicting entry", ErrUnsafeEntry)
			}
			_, copyErr := io.Copy(file, &contextReader{ctx, archive})
			closeErr := file.Close()
			if copyErr != nil {
				return out, copyErr
			}
			if closeErr != nil {
				return out, closeErr
			}
		default:
			return out, ErrUnsafeEntry
		}
	}
	// Read gzip trailer to detect truncated streams and cap concatenated payloads.
	_, err = io.Copy(io.Discard, expanded)
	if err != nil {
		return out, err
	}
	if bounded.N <= 0 {
		return out, ErrArchiveTooLarge
	}
	if expanded.N <= 0 {
		return out, ErrExpandedTooLarge
	}
	repositoryRoot, err := ResolveRepositoryRoot(extracted)
	if err != nil {
		return out, err
	}
	return Outcome{extracted, repositoryRoot, root, shaPattern.FindString(filepath.Base(repositoryRoot))}, nil
}

func ResolveRepositoryRoot(extracted string) (string, error) {
	entries, err := os.ReadDir(extracted)
	if err != nil {
		return "", err
	}
	var dirs []string
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".") && entry.IsDir() {
			dirs = append(dirs, entry.Name())
		}
	}
	if len(dirs) == 1 {
		return filepath.Join(extracted, dirs[0]), nil
	}
	return extracted, nil
}

// FindSkillFiles skips hidden trees and symlinks, matching the Swift scanner.
func FindSkillFiles(ctx context.Context, root string, max int) ([]string, error) {
	if max <= 0 {
		max = DefaultLimits().SkillFiles
	}
	var files []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if p != root && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.Type().IsRegular() && d.Name() == "SKILL.md" {
			files = append(files, p)
			if len(files) >= max {
				return filepath.SkipAll
			}
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

// ResolveCommitSHA is the GitHub API fallback when a tarball layout omits a full
// commit SHA. HTTP rejection means unresolved; transport/cancellation failures
// remain errors so a failed network operation is not hidden as a valid revision.
func (f Fetcher) ResolveCommitSHA(ctx context.Context, owner, repository, ref, token string) (string, error) {
	endpoint, err := ArchiveURL(owner, repository, ref)
	if err != nil {
		return "", err
	}
	endpoint = strings.Replace(endpoint, "/tarball/", "/commits/", 1)
	ctx, cancel := context.WithTimeout(ctx, f.Limits.defaults().FetchTimeout)
	defer cancel()
	data, err := f.download(ctx, endpoint, token, 1<<20)
	if err != nil {
		var status *StatusError
		if errors.As(err, &status) {
			return "", nil
		}
		return "", err
	}
	var result struct {
		SHA string `json:"sha"`
	}
	if json.Unmarshal(data, &result) != nil || len(result.SHA) != 40 || shaPattern.FindString(result.SHA) != result.SHA {
		return "", nil
	}
	return result.SHA, nil
}
