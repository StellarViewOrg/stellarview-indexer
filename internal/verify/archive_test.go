package verify

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testLimits() ExtractLimits {
	return ExtractLimits{
		MaxFiles:      100,
		MaxTotalBytes: 1 << 20,
		MaxFileSize:   512 << 10,
		MaxRatio:      150,
		MaxDepth:      16,
	}
}

func buildZip(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		fh := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		fh.SetMode(e.mode)
		f, err := w.CreateHeader(fh)
		if err != nil {
			t.Fatalf("create zip entry: %v", err)
		}
		if _, err := f.Write(e.data); err != nil {
			t.Fatalf("write zip entry: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

type zipEntry struct {
	name string
	data []byte
	mode os.FileMode
}

func regularZipEntry(name string, data string) zipEntry {
	return zipEntry{name: name, data: []byte(data), mode: 0o644}
}

func TestExtractZipHappyPathStripsCommonRoot(t *testing.T) {
	data := buildZip(t, []zipEntry{
		{name: "my-contract/"},
		{name: "my-contract/Cargo.toml", data: "[package]\nname = \"x\"\n"},
		{name: "my-contract/src/lib.rs", data: "pub fn hello() {}\n"},
	})

	dest := t.TempDir()
	files, err := ExtractArchive(data, dest, testLimits())
	if err != nil {
		t.Fatalf("ExtractArchive: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d: %v", len(files), files)
	}
	for _, want := range []string{"Cargo.toml", "src/lib.rs"} {
		if _, err := os.Stat(filepath.Join(dest, want)); err != nil {
			t.Errorf("expected extracted file %s: %v", want, err)
		}
	}
}

func TestExtractTarGzHappyPath(t *testing.T) {
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	addTarFile := func(name, content string) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar body: %v", err)
		}
	}
	addTarFile("Cargo.toml", "[package]\n")
	addTarFile("src/lib.rs", "// lib")
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}

	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	if _, err := gw.Write(tarBuf.Bytes()); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	dest := t.TempDir()
	files, err := ExtractArchive(gzBuf.Bytes(), dest, testLimits())
	if err != nil {
		t.Fatalf("ExtractArchive: %v", err)
	}
	if len(files) != 2 || files[0] != "Cargo.toml" || files[1] != "src/lib.rs" {
		t.Fatalf("unexpected file list: %v", files)
	}
	content, err := os.ReadFile(filepath.Join(dest, "src", "lib.rs"))
	if err != nil || string(content) != "// lib" {
		t.Fatalf("extracted content mismatch: %q err=%v", content, err)
	}
}

func TestRejectsPathTraversal(t *testing.T) {
	data := buildZip(t, []zipEntry{regularZipEntry("../evil.txt", "pwned")})
	err := extractForTest(t, data)
	if err == nil || !IsUnsafeArchive(err) || !strings.Contains(err.Error(), "traversal") {
		t.Fatalf("expected traversal rejection, got %v", err)
	}
}

func TestRejectsAbsolutePath(t *testing.T) {
	data := buildZip(t, []zipEntry{regularZipEntry("/etc/evil.txt", "pwned")})
	err := extractForTest(t, data)
	if err == nil || !IsUnsafeArchive(err) {
		t.Fatalf("expected absolute path rejection, got %v", err)
	}
}

func TestRejectsSymlinkZipEntry(t *testing.T) {
	data := buildZip(t, []zipEntry{
		{name: "link", data: []byte("/etc/passwd"), mode: os.ModeSymlink | 0o777},
	})
	err := extractForTest(t, data)
	if err == nil || !IsUnsafeArchive(err) || !strings.Contains(err.Error(), "non-regular") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestRejectsSymlinkTarEntry(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "link", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink, Mode: 0o777}); err != nil {
		t.Fatalf("header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	err := extractForTest(t, buf.Bytes())
	if err == nil || !IsUnsafeArchive(err) {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestRejectsZipBombByRatio(t *testing.T) {
	zeros := bytes.Repeat([]byte{0}, 8<<20)
	data := buildZip(t, []zipEntry{regularZipEntry("zeros.bin", string(zeros))})
	limits := testLimits()
	limits.MaxTotalBytes = 64 << 20
	limits.MaxFileSize = 32 << 20
	err := extractForTest(t, data, limits)
	if err == nil || !IsUnsafeArchive(err) || !strings.Contains(err.Error(), "ratio") {
		t.Fatalf("expected bomb rejection, got %v", err)
	}
}

func TestRejectsOversizedTotalUncompressed(t *testing.T) {
	payload := strings.Repeat("a", 600<<10)
	data := buildZip(t, []zipEntry{
		regularZipEntry("a.txt", payload),
		regularZipEntry("b.txt", payload),
	})
	limits := testLimits()
	limits.MaxRatio = 5000
	err := extractForTest(t, data, limits)
	if err == nil || !IsUnsafeArchive(err) || !strings.Contains(err.Error(), "total uncompressed") {
		t.Fatalf("expected size cap rejection, got %v", err)
	}
}

func TestRejectsTooManyFiles(t *testing.T) {
	var entries []zipEntry
	for i := 0; i < 150; i++ {
		entries = append(entries, regularZipEntry(fmt.Sprintf("f%d.txt", i), "x"))
	}
	data := buildZip(t, entries)
	err := extractForTest(t, data)
	if err == nil || !IsUnsafeArchive(err) || !strings.Contains(err.Error(), "too many files") {
		t.Fatalf("expected too-many-files rejection, got %v", err)
	}
}

func TestRejectsUnsupportedFormat(t *testing.T) {
	err := extractForTest(t, []byte("this is not an archive at all........"))
	if err == nil || !IsUnsafeArchive(err) || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected unsupported format rejection, got %v", err)
	}
}

func TestRejectsEmptyArchive(t *testing.T) {
	err := extractForTest(t, nil)
	if err == nil || !IsUnsafeArchive(err) {
		t.Fatalf("expected empty rejection, got %v", err)
	}
}

func TestDetectArchiveFormat(t *testing.T) {
	if got := DetectArchiveFormat([]byte("PK\x03\x04rest")); got != "zip" {
		t.Errorf("zip detection failed: %q", got)
	}
	if got := DetectArchiveFormat([]byte{0x1f, 0x8b, 0x00}); got != "gzip" {
		t.Errorf("gzip detection failed: %q", got)
	}
	ustar := make([]byte, 600)
	copy(ustar[257:], "ustar\x0000")
	if got := DetectArchiveFormat(ustar); got != "tar" {
		t.Errorf("tar detection failed: %q", got)
	}
	if got := DetectArchiveFormat([]byte("nope")); got != "" {
		t.Errorf("expected empty detection, got %q", got)
	}
}

func extractForTest(t *testing.T, data []byte, limits ...ExtractLimits) error {
	t.Helper()
	l := testLimits()
	if len(limits) > 0 {
		l = limits[0]
	}
	_, err := ExtractArchive(data, t.TempDir(), l)
	return err
}
