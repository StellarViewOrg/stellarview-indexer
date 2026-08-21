package verify

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

type ExtractLimits struct {
	MaxFiles      int
	MaxTotalBytes int64
	MaxFileSize   int64
	MaxRatio      float64
	MaxDepth      int
}

func DefaultExtractLimits(maxTotalBytes int64) ExtractLimits {
	return ExtractLimits{
		MaxFiles:      4096,
		MaxTotalBytes: maxTotalBytes,
		MaxFileSize:   8 << 20,
		MaxRatio:      150,
		MaxDepth:      24,
	}
}

func unsafeErr(format string, args ...interface{}) error {
	return fmt.Errorf("unsafe archive: %s", fmt.Sprintf(format, args...))
}

func IsUnsafeArchive(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unsafe archive:")
}

func DetectArchiveFormat(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("PK\x03\x04")),
		bytes.HasPrefix(data, []byte("PK\x05\x06")),
		bytes.HasPrefix(data, []byte("PK\x07\x08")):
		return "zip"
	case bytes.HasPrefix(data, []byte{0x1f, 0x8b}):
		return "gzip"
	case len(data) >= 262 && string(data[257:262]) == "ustar":
		return "tar"
	default:
		return ""
	}
}

func ExtractArchive(data []byte, dest string, limits ExtractLimits) ([]string, error) {
	if len(data) == 0 {
		return nil, unsafeErr("empty submission")
	}
	switch DetectArchiveFormat(data) {
	case "zip":
		return extractZip(data, dest, limits)
	case "gzip":
		return extractTarGz(data, dest, limits)
	case "tar":
		return extractTarPasses(func() io.Reader { return bytes.NewReader(data) }, dest, limits)
	default:
		return nil, unsafeErr("unsupported archive format (expected .zip, .tar.gz or .tar)")
	}
}

func sanitizeRelPath(name string, limits ExtractLimits) (string, error) {
	name = strings.TrimSuffix(name, "/")
	if name == "" || name == "." {
		return "", unsafeErr("empty entry path")
	}
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "\\") {
		return "", unsafeErr("absolute path %q not allowed", name)
	}
	if len(name) >= 2 && name[1] == ':' {
		return "", unsafeErr("drive-letter path %q not allowed", name)
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." {
			return "", unsafeErr("path traversal in %q not allowed", name)
		}
	}
	cleaned := path.Clean(name)
	if limits.MaxDepth > 0 && strings.Count(cleaned, "/")+1 > limits.MaxDepth {
		return "", unsafeErr("path %q exceeds maximum depth", name)
	}
	return cleaned, nil
}

func stripCommonRoot(names []string) string {
	if len(names) == 0 {
		return ""
	}
	parts := strings.SplitN(names[0], "/", 2)
	if len(parts) < 2 || parts[0] == "" {
		return ""
	}
	root := parts[0] + "/"
	for _, n := range names[1:] {
		if !strings.HasPrefix(n, root) {
			return ""
		}
	}
	return root
}

type extractGuard struct {
	limits ExtractLimits
	files  int
	total  int64
}

func (g *extractGuard) checkFile(rel string, declared int64) error {
	g.files++
	if g.limits.MaxFiles > 0 && g.files > g.limits.MaxFiles {
		return unsafeErr("too many files (limit %d)", g.limits.MaxFiles)
	}
	if declared > g.limits.MaxFileSize {
		return unsafeErr("file %q exceeds per-file size limit", rel)
	}
	if g.limits.MaxTotalBytes > 0 && g.total+declared > g.limits.MaxTotalBytes {
		return unsafeErr("archive exceeds total uncompressed size limit")
	}
	g.total += declared
	return nil
}

type countingReader struct {
	r   io.Reader
	n   int64
	cap int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	if c.cap > 0 && c.n > c.cap {
		return n, unsafeErr("archive exceeds total uncompressed size limit")
	}
	return n, err
}

func writeFileSafe(destDir, rel string, r io.Reader, size int64) (int64, error) {
	full := filepath.Join(destDir, filepath.FromSlash(rel))
	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	written, err := io.Copy(f, io.LimitReader(r, size+1))
	if err != nil {
		return written, err
	}
	if written > size {
		return written, unsafeErr("file %q larger than declared size", rel)
	}
	return written, nil
}

func extractZip(data []byte, dest string, limits ExtractLimits) ([]string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, unsafeErr("invalid zip: %v", err)
	}
	if len(zr.File) == 0 {
		return nil, unsafeErr("archive contains no files")
	}

	var names []string
	var totalUncompressed, totalCompressed int64
	for _, f := range zr.File {
		rel, err := sanitizeRelPath(f.Name, limits)
		if err != nil {
			return nil, err
		}
		names = append(names, rel)
		totalUncompressed += int64(f.UncompressedSize64)
		totalCompressed += int64(f.CompressedSize64)
	}
	if limits.MaxTotalBytes > 0 && totalUncompressed > limits.MaxTotalBytes {
		return nil, unsafeErr("archive exceeds total uncompressed size limit")
	}
	if totalCompressed > 1024 && float64(totalUncompressed)/float64(totalCompressed) > limits.MaxRatio {
		return nil, unsafeErr("compression ratio exceeds limit (possible zip bomb)")
	}

	root := stripCommonRoot(names)
	guard := &extractGuard{limits: limits}

	var extracted []string
	for _, f := range zr.File {
		rel, err := sanitizeRelPath(f.Name, limits)
		if err != nil {
			return nil, err
		}
		rel = strings.TrimPrefix(rel, root)
		if rel == "" {
			continue
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(filepath.Join(dest, filepath.FromSlash(rel)), 0o755); err != nil {
				return nil, err
			}
			continue
		}
		if f.Mode()&os.ModeType != 0 {
			return nil, unsafeErr("non-regular file entry %q (symlink/device/fifo) not allowed", f.Name)
		}
		if err := guard.checkFile(rel, int64(f.UncompressedSize64)); err != nil {
			return nil, err
		}
		rc, err := f.Open()
		if err != nil {
			return nil, unsafeErr("cannot open zip entry %q: %v", f.Name, err)
		}
		_, werr := writeFileSafe(dest, rel, rc, int64(f.UncompressedSize64))
		rc.Close()
		if werr != nil {
			return nil, werr
		}
		extracted = append(extracted, rel)
	}
	if len(extracted) == 0 {
		return nil, unsafeErr("archive contains no files")
	}
	sort.Strings(extracted)
	return extracted, nil
}

func extractTarGz(data []byte, dest string, limits ExtractLimits) ([]string, error) {
	return extractTarPasses(func() io.Reader {
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return errReader{err: unsafeErr("invalid gzip stream: %v", err)}
		}
		return &countingReader{r: gz, cap: limits.MaxTotalBytes + (1 << 20)}
	}, dest, limits)
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func extractTarPasses(open func() io.Reader, dest string, limits ExtractLimits) ([]string, error) {
	names, err := tarCollectNames(open(), limits)
	if err != nil {
		return nil, err
	}
	if len(names.files) == 0 {
		return nil, unsafeErr("archive contains no files")
	}

	root := stripCommonRoot(append(append([]string{}, names.dirs...), names.files...))
	guard := &extractGuard{limits: limits}

	tr := tar.NewReader(open())
	var extracted []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, unsafeErr("invalid tar stream: %v", err)
		}
		rel, err := sanitizeRelPath(hdr.Name, limits)
		if err != nil {
			return nil, err
		}
		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			rel = strings.TrimPrefix(rel, root)
			if rel == "" {
				continue
			}
			if err := guard.checkFile(rel, hdr.Size); err != nil {
				return nil, err
			}
			if _, err := writeFileSafe(dest, rel, tr, hdr.Size); err != nil {
				return nil, err
			}
			extracted = append(extracted, rel)
		case tar.TypeDir:
			rel = strings.TrimPrefix(rel, root)
			if rel == "" {
				continue
			}
			if err := os.MkdirAll(filepath.Join(dest, filepath.FromSlash(rel)), 0o755); err != nil {
				return nil, err
			}
		default:
			return nil, unsafeErr("unexpected entry type in tar stream")
		}
	}
	if len(extracted) == 0 {
		return nil, unsafeErr("archive contains no files")
	}
	sort.Strings(extracted)
	return extracted, nil
}

type tarNames struct {
	files []string
	dirs  []string
}

func tarCollectNames(r io.Reader, limits ExtractLimits) (*tarNames, error) {
	tr := tar.NewReader(r)
	names := &tarNames{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, unsafeErr("invalid tar stream: %v", err)
		}
		rel, err := sanitizeRelPath(hdr.Name, limits)
		if err != nil {
			return nil, err
		}
		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			names.files = append(names.files, rel)
		case tar.TypeDir:
			names.dirs = append(names.dirs, rel)
		default:
			return nil, unsafeErr("non-regular file entry %q (symlink/hardlink/device/fifo) not allowed", hdr.Name)
		}
	}
	return names, nil
}
