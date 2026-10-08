// Package projectzip moves an Ignition project between a directory and the zip the
// Gateway's project import and export endpoints speak.
//
// A project directory is zipped deterministically: entries in path order, one fixed
// modification time, and no directory entries, so the same tree always yields the
// same bytes. An export is unpacked under the same bounds a module archive is read
// under, and an entry that would land outside the target directory is refused.
package projectzip

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ManifestFile is the file every Ignition project carries at its root.
const ManifestFile = "project.json"

// Bounds an import or export stays inside: a project past them is refused, never
// cut.
const (
	// MaxBytes bounds a project zip and the total size of the files it expands to.
	MaxBytes = 256 << 20
	// MaxCompressionRatio bounds how far one entry may expand relative to its
	// stored size.
	MaxCompressionRatio = 200
)

// epoch is the modification time every zipped entry carries, so the archive does
// not change when only file times do.
var epoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// Zip packs the project directory dir. It refuses a directory without a
// project.json at its root, a symlink, and a tree larger than MaxBytes.
func Zip(dir string) ([]byte, error) {
	info, err := os.Stat(filepath.Join(dir, ManifestFile))
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s has no %s at its root", dir, ManifestFile)
	}
	var files []string
	var total int64
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", p)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if total > MaxBytes {
			return fmt.Errorf("the project is over the %d-byte limit", MaxBytes)
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, name := range files {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			return nil, err
		}
		entry, err := w.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: epoch})
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write(data); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Check reads a project zip and refuses one that is not a zip, that has no
// project.json at its root, or whose entries break the bounds or the path rule.
func Check(data []byte) error {
	_, err := entries(data)
	return err
}

// Unpack writes the project zip into dir, which ends up holding exactly the
// zip's files: a file under dir the zip does not carry is removed, so an export
// over a tracked project directory shows deletions in git too. dir has to be
// absent, empty, or already a project directory (holding a project.json), so an
// export never clears a directory that is not a project.
func Unpack(data []byte, dir string) ([]string, error) {
	files, err := entries(data)
	if err != nil {
		return nil, err
	}
	if err := unpackable(dir); err != nil {
		return nil, err
	}
	keep := map[string]bool{}
	names := make([]string, 0, len(files))
	for _, f := range files {
		keep[f.Name] = true
		names = append(names, f.Name)
		target := filepath.Join(dir, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, err
		}
		if err := writeEntry(f, target); err != nil {
			return nil, err
		}
	}
	if err := prune(dir, keep); err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

// unpackable refuses a target directory that holds something other than a project.
func unpackable(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && len(entries) == 0) {
		return nil
	}
	if err != nil {
		return err
	}
	if info, err := os.Stat(filepath.Join(dir, ManifestFile)); err == nil && info.Mode().IsRegular() {
		return nil
	}
	return fmt.Errorf("%s is not empty and holds no %s, so it is not a project directory to replace", dir, ManifestFile)
}

// entries opens the zip and returns its file entries, checked.
func entries(data []byte) ([]*zip.File, error) {
	if int64(len(data)) > MaxBytes {
		return nil, fmt.Errorf("the project zip is over the %d-byte limit", MaxBytes)
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("it is not a zip archive: %v", err)
	}
	var (
		out      []*zip.File
		total    uint64
		manifest bool
	)
	for _, f := range r.File {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		if reason := entryProblem(f.Name); reason != "" {
			return nil, fmt.Errorf("entry %q %s", f.Name, reason)
		}
		total += f.UncompressedSize64
		if total > MaxBytes {
			return nil, fmt.Errorf("the project expands past the %d-byte limit", MaxBytes)
		}
		if f.CompressedSize64 > 0 && f.UncompressedSize64/f.CompressedSize64 > MaxCompressionRatio {
			return nil, fmt.Errorf("entry %q is stored at a ratio past %d:1", f.Name, MaxCompressionRatio)
		}
		if f.Name == ManifestFile {
			manifest = true
		}
		out = append(out, f)
	}
	if !manifest {
		return nil, fmt.Errorf("it has no %s at its root", ManifestFile)
	}
	return out, nil
}

// entryProblem says why a zip entry name cannot be written under a directory.
func entryProblem(name string) string {
	switch {
	case name == "", strings.HasPrefix(name, "/"), strings.Contains(name, `\`):
		return "is not a relative path"
	case path.Clean(name) != name, name == "..", strings.HasPrefix(name, "../"):
		return "leaves the project directory"
	}
	return ""
}

// writeEntry writes one entry, reading no more than it declares.
func writeEntry(f *zip.File, target string) error {
	src, err := f.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(src, int64(f.UncompressedSize64))); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// prune removes the files under dir the export did not carry, then the
// directories that leaves empty.
func prune(dir string, keep map[string]bool) error {
	var dirs []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
			return nil
		}
		if !keep[filepath.ToSlash(rel)] {
			return os.Remove(p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if entries, err := os.ReadDir(dirs[i]); err == nil && len(entries) == 0 {
			_ = os.Remove(dirs[i])
		}
	}
	return nil
}
