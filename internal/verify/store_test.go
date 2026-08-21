package verify

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

func getTestStore(t *testing.T) *PGStore {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		url = "postgresql://explorer:explorer_dev@localhost:54320/stellar_explorer?sslmode=disable"
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Skipf("Skipping: cannot open test database: %v", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		t.Skipf("Skipping: cannot connect to test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewPGStore(db)
}

func seedVerificationFixture(t *testing.T, st *PGStore, contractID string, wasmHash *string) {
	t.Helper()
	ctx := context.Background()
	_, err := st.db.ExecContext(ctx, `
		INSERT INTO contracts (contract_id, wasm_hash, created_ledger, created_at, last_modified_ledger)
		VALUES ($1,$2,1,NOW(),1)
		ON CONFLICT (contract_id) DO UPDATE SET wasm_hash = EXCLUDED.wasm_hash`,
		contractID, wasmHash)
	if err != nil {
		t.Fatalf("seed contract: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.db.ExecContext(context.Background(), "DELETE FROM contracts WHERE contract_id = $1", contractID)
	})
}

func TestPGVerificationRoundTrip(t *testing.T) {
	st := getTestStore(t)
	ctx := context.Background()

	contractID := strings.ToLower("C" + newUUIDv4())
	hash := strings.Repeat("aa", 32)
	seedVerificationFixture(t, st, contractID, &hash)

	expected, err := st.ExpectedWasmHash(ctx, contractID)
	if err != nil || expected == nil || *expected != hash {
		t.Fatalf("ExpectedWasmHash: %v %v", expected, err)
	}

	v := &Verification{
		ID:               newUUIDv4(),
		ContractID:       contractID,
		Network:          "testnet",
		Status:           StatusQueued,
		ExpectedWasmHash: expected,
		Source: SourceSpec{
			Type: SourceTypeArchive, Name: "src.tar.gz",
			SHA256: strings.Repeat("bb", 32),
		},
		ToolchainRequested: Toolchain{Rust: "1.85.0"},
		Build:              BuildSpec{Profile: ProfileRelease},
		CreatedAt:          time.Now().UTC(),
	}
	if err := st.CreateVerification(ctx, v); err != nil {
		t.Fatalf("CreateVerification: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.db.ExecContext(context.Background(),
			"DELETE FROM contract_verifications WHERE id = $1::uuid", v.ID)
	})

	got, err := st.GetVerification(ctx, v.ID)
	if err != nil {
		t.Fatalf("GetVerification: %v", err)
	}
	if got.Status != StatusQueued || got.Source.Name != "src.tar.gz" ||
		got.ToolchainRequested.Rust != "1.85.0" || got.Build.Profile != ProfileRelease {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if _, err := st.GetLogs(ctx, v.ID); err != nil {
		t.Fatalf("GetLogs: %v", err)
	}

	if err := st.SetBuilding(ctx, v.ID); err != nil {
		t.Fatalf("SetBuilding: %v", err)
	}

	err = st.Complete(ctx, &Completion{
		ID: v.ID, Status: StatusVerified, Match: true,
		WasmHash: hash, Network: "testnet",
		Toolchain: &Toolchain{Rust: "1.85.0", StellarCLI: "22.0.8"},
		Logs:      "build ok", DurationMS: 1234,
		Files: []SourceFile{
			{Path: "Cargo.toml", Content: []byte("[package]")},
			{Path: "src/lib.rs", Content: []byte("// lib")},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	done, err := st.GetVerification(ctx, v.ID)
	if err != nil {
		t.Fatalf("GetVerification after complete: %v", err)
	}
	if done.Status != StatusVerified || done.Match == nil || !*done.Match ||
		done.WasmHash == nil || *done.WasmHash != hash || done.CompletedAt == nil {
		t.Fatalf("completion state wrong: %+v", done)
	}

	vs, err := st.GetVerifiedSource(ctx, hash)
	if err != nil {
		t.Fatalf("GetVerifiedSource: %v", err)
	}
	if vs.VerificationID != v.ID || vs.FileCount != 2 {
		t.Fatalf("verified source mismatch: %+v", vs)
	}
	files, err := st.ListSourceFiles(ctx, hash)
	if err != nil || len(files) != 2 || files[0].Path != "Cargo.toml" {
		t.Fatalf("ListSourceFiles: %v %+v", err, files)
	}
	f, err := st.GetSourceFile(ctx, hash, "src/lib.rs")
	if err != nil || string(f.Content) != "// lib" {
		t.Fatalf("GetSourceFile: %v %q", err, f)
	}

	t.Cleanup(func() {
		_, _ = st.db.ExecContext(context.Background(), "DELETE FROM source_files WHERE wasm_hash = $1", hash)
		_, _ = st.db.ExecContext(context.Background(), "DELETE FROM verified_sources WHERE wasm_hash = $1", hash)
	})
}

func TestPGVerifiedSourcesShareOneRecordPerWasmHash(t *testing.T) {
	st := getTestStore(t)
	ctx := context.Background()

	hash := strings.Repeat("cc", 32)
	c1 := strings.ToLower("C" + newUUIDv4())
	c2 := strings.ToLower("C" + newUUIDv4())
	seedVerificationFixture(t, st, c1, &hash)
	seedVerificationFixture(t, st, c2, &hash)

	mk := func(contractID string) *Verification {
		v := &Verification{
			ID:         newUUIDv4(),
			ContractID: contractID,
			Network:    "testnet",
			Status:     StatusQueued,
			Source:     SourceSpec{Type: SourceTypeArchive},
			Build:      BuildSpec{Profile: ProfileRelease},
			CreatedAt:  time.Now().UTC(),
		}
		if err := st.CreateVerification(ctx, v); err != nil {
			t.Fatalf("CreateVerification: %v", err)
		}
		return v
	}
	v1, v2 := mk(c1), mk(c2)
	t.Cleanup(func() {
		for _, id := range []string{v1.ID, v2.ID} {
			_, _ = st.db.ExecContext(context.Background(),
				"DELETE FROM contract_verifications WHERE id = $1::uuid", id)
		}
		_, _ = st.db.ExecContext(context.Background(), "DELETE FROM source_files WHERE wasm_hash = $1", hash)
		_, _ = st.db.ExecContext(context.Background(), "DELETE FROM verified_sources WHERE wasm_hash = $1", hash)
	})

	files := []SourceFile{{Path: "Cargo.toml", Content: []byte("[package]")}}
	if err := st.Complete(ctx, &Completion{
		ID: v1.ID, Status: StatusVerified, Match: true,
		WasmHash: hash, Network: "testnet", Files: files,
	}); err != nil {
		t.Fatalf("complete v1: %v", err)
	}
	if err := st.Complete(ctx, &Completion{
		ID: v2.ID, Status: StatusVerified, Match: true,
		WasmHash: hash, Network: "testnet", Files: files,
	}); err != nil {
		t.Fatalf("complete v2 (duplicate bytecode): %v", err)
	}

	var count int
	if err := st.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM verified_sources WHERE wasm_hash = $1", hash).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one verified_sources row for shared bytecode, got %d", count)
	}
	vs, _ := st.GetVerifiedSource(ctx, hash)
	if vs.VerificationID != v1.ID {
		t.Fatalf("first verification should own the record, got %s", vs.VerificationID)
	}
}

func TestPGFailInterrupted(t *testing.T) {
	st := getTestStore(t)
	ctx := context.Background()

	v := &Verification{
		ID:         newUUIDv4(),
		ContractID: strings.ToLower("C" + newUUIDv4()),
		Network:    "testnet",
		Status:     StatusQueued,
		Source:     SourceSpec{Type: SourceTypeArchive},
		Build:      BuildSpec{Profile: ProfileRelease},
		CreatedAt:  time.Now().UTC().Add(-10 * time.Minute),
	}
	if err := st.CreateVerification(ctx, v); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = st.db.ExecContext(context.Background(),
			"DELETE FROM contract_verifications WHERE id = $1::uuid", v.ID)
	})

	if _, err := st.FailInterrupted(ctx); err != nil {
		t.Fatalf("FailInterrupted: %v", err)
	}
	got, _ := st.GetVerification(ctx, v.ID)
	if got.Status != StatusFailed || !strings.Contains(got.Reason, "interrupted") {
		t.Fatalf("expected stale job failed, got %+v", got)
	}
}
