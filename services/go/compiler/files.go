package compiler

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type PackageFile struct {
	Path, ContentType, Checksum string
	Content                     []byte
}

// PackageFiles excludes nested skill packages and bounds all ancillary content.
func PackageFiles(ctx context.Context, directory string) ([]PackageFile, error) {
	files := []PackageFile{}
	total := 0
	err := filepath.WalkDir(directory, func(filename string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if filename == directory {
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("unsafe package symlink")
		}
		if entry.IsDir() {
			_, err := os.Lstat(filepath.Join(filename, "SKILL.md"))
			if err == nil {
				return filepath.SkipDir
			}
			if !os.IsNotExist(err) {
				return err
			}
			return nil
		}
		if entry.Name() == "SKILL.md" {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("unsafe package entry")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > 256<<10 || len(files) >= 128 || int64(total)+info.Size() > 2<<20 {
			return errors.New("package files exceed ingestion limits")
		}
		relative, err := filepath.Rel(directory, filename)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("unsafe package path")
		}
		file, err := os.Open(filename)
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, (256<<10)+1))
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if int64(len(data)) != info.Size() {
			return errors.New("package file changed during read")
		}
		kind := map[string]string{".md": "text/markdown", ".txt": "text/plain", ".json": "application/json", ".yaml": "application/yaml", ".yml": "application/yaml", ".sh": "text/x-shellscript", ".py": "text/x-python", ".js": "text/javascript", ".mjs": "text/javascript", ".ts": "text/typescript"}[strings.ToLower(filepath.Ext(relative))]
		files = append(files, PackageFile{filepath.ToSlash(relative), kind, Hash(data), data})
		total += len(data)
		return nil
	})
	return files, err
}
