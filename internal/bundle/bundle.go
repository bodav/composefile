// Package bundle creates, validates, and extracts release bundles.
//
// A bundle is a gzip-compressed tar archive whose root is:
//
//	stacks/<stack-name>/...
//
// Modes are normalized (0644/0755) so extraction is deterministic across the
// whole deployment set.
package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"composefile/internal/manifest"
)

const (
	// ArchiveDir is the top-level directory inside every bundle.
	ArchiveDir = "stacks"

	// DefaultBundleDir is the retained bundle directory relative to the manifest.
	DefaultBundleDir = ".bundle"
)

// excludedComponents are skipped at any depth during source traversal.
var excludedComponents = map[string]bool{
	".git":         true,
	".composefile": true,
	".bundle":      true,
}

// BundleName returns the timestamped bundle filename for a manifest.
func BundleName(m *manifest.Manifest, at time.Time) string {
	return at.UTC().Format("20060102T150405Z") + "-" + m.Name + ".tar.gz"
}

// Build creates ./.bundle/<BundleName> containing every stack source.
// outDir is created if needed. The bundle is never overwritten.
func Build(m *manifest.Manifest, outDir string) (string, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("bundle: create %s: %w", outDir, err)
	}
	path := filepath.Join(outDir, BundleName(m, time.Now()))
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("bundle: %s already exists; refusing to overwrite retained bundles", path)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", fmt.Errorf("bundle: create %s: %w", path, err)
	}
	closeErr := func() error { return f.Close() }

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	for i := range m.Stacks {
		s := &m.Stacks[i]
		if err := writeStack(tw, s); err != nil {
			gz.Close()
			closeErr()
			os.Remove(path)
			return "", fmt.Errorf("bundle: stack %q: %w", s.Name, err)
		}
	}

	if err := tw.Close(); err != nil {
		gz.Close()
		closeErr()
		os.Remove(path)
		return "", fmt.Errorf("bundle: finalize: %w", err)
	}
	if err := gz.Close(); err != nil {
		closeErr()
		os.Remove(path)
		return "", fmt.Errorf("bundle: finalize gzip: %w", err)
	}
	if err := closeErr(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("bundle: close: %w", err)
	}
	return path, nil
}

// writeStack archives one stack source into tw under stacks/<name>/.
func writeStack(tw *tar.Writer, s *manifest.Stack) error {
	root := s.SourceAbs
	prefix := filepath.ToSlash(ArchiveDir) + "/" + s.Name + "/"

	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if excludedComponents[filepath.Base(path)] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		link := ""
		if d.Type()&fs.ModeSymlink != 0 {
			link, err = os.Readlink(path)
			if err != nil {
				return err
			}
			if err := checkSymlink(root, path, link); err != nil {
				return err
			}
		} else if !d.Type().IsRegular() && !d.IsDir() {
			return fmt.Errorf("unsupported file type %s (%s)", d.Type(), path)
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		hdr := &tar.Header{
			Name:     prefix + filepath.ToSlash(rel),
			ModTime:  info.ModTime(),
			Typeflag: tar.TypeReg,
			Mode:     0o644,
		}

		switch {
		case d.IsDir():
			hdr.Typeflag = tar.TypeDir
			hdr.Name += "/"
			hdr.Mode = 0o755
		case d.Type()&fs.ModeSymlink != 0:
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = link
			hdr.Mode = 0o777
		default:
			if info.Mode().Perm()&0o111 != 0 {
				hdr.Mode = 0o755
			}
			hdr.Size = info.Size()
		}

		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeReg {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tw, file)
			file.Close()
			if copyErr != nil {
				return copyErr
			}
		}
		return nil
	})
}

// checkSymlink ensures the resolved target of path stays inside root.
func checkSymlink(root, path, link string) error {
	absTarget := link
	if !filepath.IsAbs(absTarget) {
		absTarget = filepath.Join(filepath.Dir(path), link)
	}
	resolved, err := filepath.EvalSymlinks(absTarget)
	if err != nil {
		return fmt.Errorf("symlink %s: unresolvable target %q", path, link)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return err
	}
	if rel == ".." || len(rel) > 2 && rel[:3] == ".."+string(filepath.Separator) {
		return fmt.Errorf("symlink %s escapes source root (target %s)", path, link)
	}
	return nil
}

// Validate opens archivePath and checks it contains every manifest stack.
func Validate(archivePath string, m *manifest.Manifest) error {
	r, err := openArchive(archivePath)
	if err != nil {
		return err
	}
	defer r.Close()

	found := make(map[string]bool, len(m.Stacks))
	for _, s := range m.Stacks {
		found[s.Name] = false
	}

	seen := map[string]bool{}
	for {
		hdr, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("bundle: read %s: %w", archivePath, err)
		}
		name := filepath.Clean(hdr.Name)
		if name == "." || name == ".." || filepath.IsAbs(name) || len(name) > 3 && name[:3] == ".."+string(filepath.Separator) {
			return fmt.Errorf("bundle: unsafe archive path %q", hdr.Name)
		}
		if seen[name] {
			return fmt.Errorf("bundle: duplicate archive path %q", hdr.Name)
		}
		seen[name] = true
		if rest, ok := stackPrefix(hdr.Name); ok {
			found[rest] = true
		}
	}
	for name, ok := range found {
		if !ok {
			return fmt.Errorf("bundle: missing stack %q in archive", name)
		}
	}
	return nil
}

// stackPrefix reports the stack name if hdr.Name is under stacks/<name>/.
func stackPrefix(name string) (string, bool) {
	const p = ArchiveDir + "/"
	clean := filepath.ToSlash(name)
	if len(clean) <= len(p) || clean[:len(p)] != p {
		return "", false
	}
	rest := clean[len(p):]
	idx := strings.IndexByte(rest, '/')
	if idx < 0 {
		return "", false
	}
	return rest[:idx], true
}

// ExtractStack writes the source of stackName from archivePath into destDir.
// The destination directory must already exist.
func ExtractStack(archivePath, stackName, destDir string) error {
	r, err := openArchive(archivePath)
	if err != nil {
		return err
	}
	defer r.Close()

	prefix := ArchiveDir + "/" + stackName + "/"

	for {
		hdr, err := r.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if len(hdr.Name) < len(prefix) || hdr.Name[:len(prefix)] != prefix {
			continue
		}
		rel := filepath.FromSlash(hdr.Name[len(prefix):])
		target := filepath.Join(destDir, rel)
		if err := writeEntry(target, hdr, r); err != nil {
			return fmt.Errorf("extract %s: %w", hdr.Name, err)
		}
	}
}

func writeEntry(target string, hdr *tar.Header, r io.Reader) error {
	switch hdr.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, os.FileMode(hdr.Mode))
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode))
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(f, r)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	case tar.TypeSymlink:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		os.Remove(target)
		return os.Symlink(hdr.Linkname, target)
	default:
		return errors.New("unsupported tar entry type")
	}
}

// ChangeKind classifies a single path difference between two bundles.
type ChangeKind int

const (
	// ChangeAdd means the path is present in the new bundle but not the old.
	ChangeAdd ChangeKind = iota
	// ChangeModify means the path differs in content or normalized mode.
	ChangeModify
	// ChangeDelete means the path is present in the old bundle but not the new.
	ChangeDelete
)

func (k ChangeKind) String() string {
	switch k {
	case ChangeAdd:
		return "A"
	case ChangeModify:
		return "M"
	case ChangeDelete:
		return "D"
	default:
		return "?"
	}
}

// Change is one path difference between two bundles. Rel is the path within the
// stack, mirroring its layout under stacks/<Stack>/<Rel>.
type Change struct {
	Kind  ChangeKind
	Stack string
	Rel   string
}

// bEntry is the comparably-relevant content of one bundle archive entry.
type bEntry struct {
	typ    byte
	mode   int64
	data   []byte
	target string
}

// archiveEntries reads every regular file and symlink from a bundle into a map
// keyed by its normalized path (stacks/<stack>/<Rel>). Directories are omitted:
// their existence is implied by the entries they contain.
func archiveEntries(path string) (map[string]bEntry, error) {
	r, err := openArchive(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	entries := make(map[string]bEntry)
	for {
		hdr, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
			data, err := io.ReadAll(r)
			if err != nil {
				return nil, err
			}
			entries[filepath.Clean(hdr.Name)] = bEntry{
				typ:  tar.TypeReg,
				data: data,
				mode: hdr.Mode & 0o777,
			}
		case tar.TypeSymlink:
			entries[filepath.Clean(hdr.Name)] = bEntry{typ: tar.TypeSymlink, target: hdr.Linkname}
		}
	}
	return entries, nil
}

// Compare reports the file-level differences between two bundles. Paths are
// compared by content bytes and normalized mode (executable vs not); timestamps
// are ignored so untouched sources never produce noise.
func Compare(newPath, oldPath string) ([]Change, error) {
	newEntries, err := archiveEntries(newPath)
	if err != nil {
		return nil, fmt.Errorf("bundle: read new %s: %w", newPath, err)
	}
	oldEntries, err := archiveEntries(oldPath)
	if err != nil {
		return nil, fmt.Errorf("bundle: read old %s: %w", oldPath, err)
	}

	var changes []Change
	for name, e := range newEntries {
		if old, ok := oldEntries[name]; !ok {
			changes = append(changes, changeFromPath(name, ChangeAdd))
		} else if !sameEntry(old, e) {
			changes = append(changes, changeFromPath(name, ChangeModify))
		}
	}
	for name := range oldEntries {
		if _, ok := newEntries[name]; !ok {
			changes = append(changes, changeFromPath(name, ChangeDelete))
		}
	}
	sortChanges(changes)
	return changes, nil
}

// sameEntry reports whether two entries are indistinguishable under bundle
// normalization.
func sameEntry(a, b bEntry) bool {
	if a.typ != b.typ {
		return false
	}
	switch a.typ {
	case tar.TypeSymlink:
		return a.target == b.target
	case tar.TypeReg:
		return bytes.Equal(a.data, b.data) && execBit(a.mode) == execBit(b.mode)
	default:
		return false
	}
}

func execBit(mode int64) int64 { return mode & 0o111 }

// changeFromPath turns a normalized path into a Change with split Stack/Rel.
func changeFromPath(name string, kind ChangeKind) Change {
	clean := filepath.ToSlash(name)
	prefix := ArchiveDir + "/"
	ofs := len(prefix)
	rest := clean
	if len(clean) > ofs && clean[:ofs] == prefix {
		rest = clean[ofs:]
	}
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return Change{Kind: kind, Stack: rest}
	}
	return Change{Kind: kind, Stack: rest[:slash], Rel: rest[slash+1:]}
}

// sortChanges gives a deterministic, grouped ordering for display and tests.
func sortChanges(changes []Change) {
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Stack != changes[j].Stack {
			return changes[i].Stack < changes[j].Stack
		}
		if changes[i].Kind != changes[j].Kind {
			return changes[i].Kind < changes[j].Kind
		}
		return changes[i].Rel < changes[j].Rel
	})
}

// archive wraps a tar reader with its underlying closers.
type archive struct {
	*tar.Reader
	closers []io.Closer
}

func (a *archive) Close() error {
	for _, c := range a.closers {
		c.Close()
	}
	return nil
}

// openArchive opens a gzip tar archive for reading.
func openArchive(path string) (*archive, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("bundle: open %s: %w", path, err)
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("bundle: gzip %s: %w", path, err)
	}
	return &archive{Reader: tar.NewReader(gz), closers: []io.Closer{gz, f}}, nil
}
