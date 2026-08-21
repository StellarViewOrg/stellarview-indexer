package verify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/strkey"
)

func sha256HexOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func testContractID(t *testing.T, seed byte) string {
	t.Helper()
	var raw [32]byte
	for i := range raw {
		raw[i] = seed + byte(i)
	}
	id, err := strkey.Encode(strkey.VersionByteContract, raw[:])
	if err != nil {
		t.Fatalf("encode contract id: %v", err)
	}
	return id
}

func newTestService(t *testing.T, st Store, b Builder) *Service {
	t.Helper()
	svc, err := NewService(ServiceConfig{
		Store:            st,
		Builder:          b,
		Limiter:          NewIPRateLimiter(1000, 1000),
		Network:          "testnet",
		WorkspaceDir:     t.TempDir(),
		MaxArchiveBytes:  1 << 20,
		ExtractLimits:    DefaultExtractLimits(4 << 20),
		QueueSize:        8,
		BuildConcurrency: 2,
		BuildTimeout:     30 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func makeZipSource(t *testing.T) []byte {
	t.Helper()
	return buildZip(t, []zipEntry{
		{name: "Cargo.toml", data: "[package]\nname = \"c\"\n", mode: 0o644},
		{name: "src/lib.rs", data: "// c\n", mode: 0o644},
	})
}

func waitForTerminal(t *testing.T, st Store, id string) *Verification {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		v, err := st.GetVerification(context.Background(), id)
		if err == nil && v.Status.Terminal() {
			return v
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("verification did not reach terminal state in time")
	return nil
}

func TestSubmitRejectsInvalidContractID(t *testing.T) {
	svc := newTestService(t, newFakeStore(), &fakeBuilder{})
	_, err := svc.Submit(context.Background(), Submission{
		ContractID: "not-a-contract",
		Archive:    makeZipSource(t),
	})
	if err == nil || errorCodeForTest(err) != "invalid_request" {
		t.Fatalf("expected invalid_request, got %v", err)
	}
}

func errorCodeForTest(err error) string {
	_, code := errorCode(err)
	return code
}

func TestSubmitRejectsUnknownAndSACContracts(t *testing.T) {
	st := newFakeStore()
	st.addContract(testContractID(t, 1), nil)
	svc := newTestService(t, st, &fakeBuilder{})

	_, err := svc.Submit(context.Background(), Submission{
		ContractID: testContractID(t, 99),
		Archive:    makeZipSource(t),
	})
	if err == nil || errorCodeForTest(err) != "unknown_contract" {
		t.Fatalf("expected unknown_contract, got %v", err)
	}

	_, err = svc.Submit(context.Background(), Submission{
		ContractID: testContractID(t, 1),
		Archive:    makeZipSource(t),
	})
	if err == nil || errorCodeForTest(err) != "sac_unsupported" {
		t.Fatalf("expected sac_unsupported, got %v", err)
	}
}

func TestSuccessfulVerificationStoresSharedRecord(t *testing.T) {
	st := newFakeStore()
	hash := sha256HexOf("fake wasm bytes")
	st.addContract(testContractID(t, 1), &hash)
	st.addContract(testContractID(t, 2), &hash)

	b := &fakeBuilder{wasm: []byte("fake wasm bytes")}
	svc := newTestService(t, st, b)
	svc.Start(1)
	defer svc.Stop(3 * time.Second)

	ctx := context.Background()
	v1, err := svc.Submit(ctx, Submission{
		ContractID:  testContractID(t, 1),
		Network:     "testnet",
		Archive:     makeZipSource(t),
		ArchiveName: "src.tar.gz",
	})
	if err != nil {
		t.Fatalf("submit 1: %v", err)
	}
	v2, err := svc.Submit(ctx, Submission{
		ContractID:  testContractID(t, 2),
		Network:     "testnet",
		Archive:     makeZipSource(t),
		ArchiveName: "src.tar.gz",
	})
	if err != nil {
		t.Fatalf("submit 2: %v", err)
	}

	r1 := waitForTerminal(t, st, v1.ID)
	r2 := waitForTerminal(t, st, v2.ID)

	for _, r := range []*Verification{r1, r2} {
		if r.Status != StatusVerified {
			t.Fatalf("expected verified, got %s (reason=%s)", r.Status, r.Reason)
		}
		if r.Match == nil || !*r.Match {
			t.Fatal("expected match=true")
		}
		if r.WasmHash == nil || *r.WasmHash != hash {
			t.Fatalf("expected wasm hash %s, got %v", hash, r.WasmHash)
		}
		if r.ToolchainActual == nil || r.ToolchainActual.Rust != "1.85.0" {
			t.Fatalf("expected actual toolchain recorded, got %+v", r.ToolchainActual)
		}
	}

	vs, err := st.GetVerifiedSource(ctx, hash)
	if err != nil {
		t.Fatalf("GetVerifiedSource: %v", err)
	}
	if vs.FileCount != 2 {
		t.Errorf("expected 2 stored files, got %d", vs.FileCount)
	}
	files, _ := st.ListSourceFiles(ctx, hash)
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	if len(paths) != 2 || paths[0] != "Cargo.toml" || paths[1] != "src/lib.rs" {
		t.Fatalf("unexpected source tree: %v", paths)
	}
	fc, err := st.GetSourceFile(ctx, hash, "src/lib.rs")
	if err != nil || string(fc.Content) != "// c\n" {
		t.Fatalf("file content mismatch: %v %q", err, fc)
	}
}

func TestMismatchMarksUnverifiedWithReason(t *testing.T) {
	st := newFakeStore()
	onChain := strings.Repeat("cd", 32)
	st.addContract(testContractID(t, 1), &onChain)

	b := &fakeBuilder{wasm: []byte("different bytecode entirely")}
	svc := newTestService(t, st, b)
	svc.Start(1)
	defer svc.Stop(3 * time.Second)

	v, err := svc.Submit(context.Background(), Submission{
		ContractID: testContractID(t, 1),
		Network:    "testnet",
		Archive:    makeZipSource(t),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	res := waitForTerminal(t, st, v.ID)
	if res.Status != StatusMismatch {
		t.Fatalf("expected mismatch, got %s", res.Status)
	}
	if res.Match == nil || *res.Match {
		t.Fatal("expected match=false")
	}
	if !strings.Contains(res.Reason, "does not match on-chain hash") {
		t.Fatalf("expected clear mismatch reason, got %q", res.Reason)
	}
	if _, err := st.GetVerifiedSource(context.Background(), onChain); err != ErrNotFound {
		t.Fatalf("mismatch must not create a verified record, got %v", err)
	}
	files, _ := st.ListSourceFiles(context.Background(), onChain)
	if files != nil {
		t.Fatalf("mismatch must not store source files, got %v", files)
	}
}

func TestBuildFailureRecordsLogsAndReason(t *testing.T) {
	st := newFakeStore()
	onChain := strings.Repeat("ef", 32)
	st.addContract(testContractID(t, 1), &onChain)

	b := &fakeBuilder{err: &BuildError{Msg: "build failed (exit status)", Logs: "error: could not compile"}}
	svc := newTestService(t, st, b)
	svc.Start(1)
	defer svc.Stop(3 * time.Second)

	v, err := svc.Submit(context.Background(), Submission{
		ContractID: testContractID(t, 1),
		Network:    "testnet",
		Archive:    makeZipSource(t),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	res := waitForTerminal(t, st, v.ID)
	if res.Status != StatusFailed {
		t.Fatalf("expected failed, got %s", res.Status)
	}
	logs, err := st.GetLogs(context.Background(), v.ID)
	if err != nil || !strings.Contains(logs, "could not compile") {
		t.Fatalf("expected build logs persisted, got %q err=%v", logs, err)
	}
}

func TestMaliciousArchiveFailsWithoutBuilding(t *testing.T) {
	st := newFakeStore()
	onChain := strings.Repeat("11", 32)
	st.addContract(testContractID(t, 1), &onChain)

	b := &fakeBuilder{wasm: []byte("x")}
	svc := newTestService(t, st, b)
	svc.Start(1)
	defer svc.Stop(3 * time.Second)

	malicious := buildZip(t, []zipEntry{regularZipEntry("../../etc/evil", "pwned")})
	v, err := svc.Submit(context.Background(), Submission{
		ContractID: testContractID(t, 1),
		Network:    "testnet",
		Archive:    malicious,
	})
	if err != nil {
		t.Fatalf("submit should be accepted at API level: %v", err)
	}

	res := waitForTerminal(t, st, v.ID)
	if res.Status != StatusFailed {
		t.Fatalf("expected failed, got %s", res.Status)
	}
	if !strings.Contains(res.Reason, "traversal") {
		t.Fatalf("expected traversal reason, got %q", res.Reason)
	}
	if b.buildCount() != 0 {
		t.Fatal("sandboxed builder must never run for rejected archives")
	}
}

func TestGitSubmissionFlow(t *testing.T) {
	origFetch := fetchGitSourceFunc
	fetchGitSourceFunc = func(_ context.Context, _ SourceSpec, dest string) error {
		return writeGitFixture(dest)
	}
	defer func() { fetchGitSourceFunc = origFetch }()

	st := newFakeStore()
	onChain := sha256HexOf("git wasm")
	st.addContract(testContractID(t, 1), &onChain)

	b := &fakeBuilder{wasm: []byte("git wasm")}
	svc := newTestService(t, st, b)
	svc.Start(1)
	defer svc.Stop(3 * time.Second)

	v, err := svc.Submit(context.Background(), Submission{
		ContractID: testContractID(t, 1),
		Network:    "testnet",
		Git: &SourceSpec{
			Type:       SourceTypeGit,
			Repository: "https://github.com/example/repo",
			Commit:     strings.Repeat("a", 40),
		},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	res := waitForTerminal(t, st, v.ID)
	if res.Status != StatusVerified {
		t.Fatalf("expected verified via git flow, got %s (%s)", res.Status, res.Reason)
	}
	if res.Source.Repository != "https://github.com/example/repo" {
		t.Fatalf("source metadata lost: %+v", res.Source)
	}
}

func writeGitFixture(dest string) error {
	if err := os.MkdirAll(filepath.Join(dest, "src"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dest, "Cargo.toml"), []byte("[package]\nname = \"c\"\n"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dest, "src", "lib.rs"), []byte("// git\n"), 0o644)
}

func TestValidateBuildSpecRules(t *testing.T) {
	cases := []struct {
		name    string
		spec    BuildSpec
		wantErr bool
	}{
		{"default profile ok", BuildSpec{}, false},
		{"release ok", BuildSpec{Profile: ProfileRelease}, false},
		{"debug rejected", BuildSpec{Profile: "debug"}, true},
		{"bad package", BuildSpec{Package: "../evil"}, true},
		{"crate dir traversal", BuildSpec{CrateDir: "../../etc"}, true},
		{"crate dir absolute", BuildSpec{CrateDir: "/etc"}, true},
		{"crate dir ok", BuildSpec{CrateDir: "contracts/token"}, false},
		{"feature bad char", BuildSpec{Features: []string{"weird feature"}}, true},
		{"flag injection", BuildSpec{Flags: []string{"--manifest-path=/etc/passwd"}}, true},
		{"flag ok", BuildSpec{Flags: []string{"--offline"}}, false},
	}
	for _, tc := range cases {
		err := validateBuildSpec(&tc.spec)
		if tc.wantErr != (err != nil) {
			t.Errorf("%s: wantErr=%v got %v", tc.name, tc.wantErr, err)
		}
	}
}

func TestQueueFullReturnsError(t *testing.T) {
	st := newFakeStore()
	svc, err := NewService(ServiceConfig{
		Store:           st,
		Builder:         &fakeBuilder{},
		Limiter:         NewIPRateLimiter(1000, 1000),
		Network:         "testnet",
		WorkspaceDir:    t.TempDir(),
		MaxArchiveBytes: 1 << 20,
		ExtractLimits:   DefaultExtractLimits(4 << 20),
		QueueSize:       1,
		BuildTimeout:    30 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	sub := Submission{
		ContractID: testContractID(t, 1),
		Network:    "testnet",
		Archive:    makeZipSource(t),
	}
	st.addContract(sub.ContractID, strPtr(strings.Repeat("33", 32)))

	svc.jobs <- &job{}
	if _, err := svc.Submit(context.Background(), sub); err != ErrQueueFull {
		t.Fatalf("expected ErrQueueFull, got %v", err)
	}
	v, _ := st.GetVerification(context.Background(), latestKey(st))
	if v == nil || v.Status != StatusFailed {
		t.Fatalf("queued-then-rejected submission should be marked failed, got %+v", v)
	}
}

func strPtr(s string) *string { return &s }

func latestKey(st *fakeStore) string {
	for k := range st.verifications {
		return k
	}
	return ""
}
