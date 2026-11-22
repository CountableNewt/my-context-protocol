package snapshotimport

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

//go:embed restore.py
var restoreScript string

// RestoreLocal publishes a new standalone SQLite database only after
// transactional restore, integrity/FK checks and an independent typed re-export
// match. It never replaces a path or touches hosted databases. Legacy snapshots
// restore baseline-only databases; v2 restores every included application table.
func RestoreLocal(ctx context.Context, artifact, output string) (*Snapshot, error) {
	if _, e := os.Lstat(output); !os.IsNotExist(e) {
		return nil, errors.New("restore output must not already exist")
	}
	snapshot, e := Read(artifact)
	if e != nil {
		return nil, e
	}
	f, e := os.CreateTemp(filepath.Dir(output), ".mcp-restore-")
	if e != nil {
		return nil, errors.New("cannot create private SQLite restore")
	}
	temp := f.Name()
	if e = f.Close(); e != nil {
		os.Remove(temp)
		return nil, errors.New("cannot prepare SQLite restore")
	}
	defer os.Remove(temp)
	defer os.Remove(temp + "-journal")
	defer os.Remove(temp + "-wal")
	defer os.Remove(temp + "-shm")
	settings, _ := json.Marshal(map[string]string{"baseline": schemaScript(snapshot.Schema)})
	cmd := exec.CommandContext(ctx, "python3", "-c", restoreScript, artifact, temp)
	cmd.Stdin = bytes.NewReader(settings)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if e = cmd.Run(); e != nil {
		return nil, errors.New("offline SQLite restore failed; no output published")
	}
	// Export has separate read-only SQLite integrity/schema/FK checks, then Read
	// computes all typed table hashes and counts independently of the writer.
	verification, e := os.CreateTemp(filepath.Dir(output), ".mcp-restore-check-")
	if e != nil {
		return nil, errors.New("cannot prepare restore verification")
	}
	verifyPath := verification.Name()
	verification.Close()
	os.Remove(verifyPath)
	defer os.Remove(verifyPath)
	restored, e := Export(ctx, temp, verifyPath)
	if e != nil || restored.ID != snapshot.ID {
		return nil, errors.New("restored SQLite typed manifest differs from snapshot")
	}
	file, e := os.OpenFile(temp, os.O_RDWR, 0600)
	if e != nil {
		return nil, errors.New("cannot sync SQLite restore")
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil || closeErr != nil {
		return nil, errors.New("cannot sync SQLite restore")
	}
	if e = os.Link(temp, output); e != nil {
		return nil, errors.New("cannot publish SQLite restore; output must be new")
	}
	return restored, nil
}
