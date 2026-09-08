package store

import (
	"context"
	"testing"
)

func TestVerificationLifecycle(t *testing.T) {
	db := getTestDB(t)
	defer db.Close()
	ctx := context.Background()

	wasmHash := "ccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc0"
	contractID := "CTESTCONTRACTVERIFICATIONXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"

	defer func() {
		_, _ = db.db.ExecContext(ctx, "DELETE FROM contract_verification_sources WHERE wasm_hash = $1", wasmHash)
		_, _ = db.db.ExecContext(ctx, "DELETE FROM contract_verifications WHERE wasm_hash = $1", wasmHash)
		_, _ = db.db.ExecContext(ctx, "DELETE FROM contract_code WHERE wasm_hash = $1", wasmHash)
	}()

	if err := db.UpsertContractCode(ctx, &ContractCode{
		WasmHash:      wasmHash,
		WasmBytecode:  []byte{0x00, 0x61, 0x73, 0x6d},
		WasmSize:      4,
		CreatedLedger: 1,
	}); err != nil {
		t.Fatalf("UpsertContractCode failed: %v", err)
	}

	exists, err := db.ContractCodeExists(ctx, wasmHash)
	if err != nil {
		t.Fatalf("ContractCodeExists failed: %v", err)
	}
	if !exists {
		t.Fatal("expected contract code to exist")
	}

	repo := "https://example.com/repo"
	v := &ContractVerification{
		WasmHash:      wasmHash,
		ContractID:    contractID,
		Network:       "testnet",
		RepositoryURL: &repo,
	}
	files := []VerificationSourceFile{
		{FilePath: "src/lib.rs", Content: "fn main() {}", SizeBytes: 12},
		{FilePath: "Cargo.toml", Content: "[package]\nname = \"x\"", SizeBytes: 20},
	}

	id, err := db.CreateVerification(ctx, v, files)
	if err != nil {
		t.Fatalf("CreateVerification failed: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero id")
	}

	got, err := db.GetLatestVerificationByWasmHash(ctx, wasmHash)
	if err != nil {
		t.Fatalf("GetLatestVerificationByWasmHash failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected a verification record")
	}
	if got.Status != VerificationStatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
	if got.ContractID != contractID {
		t.Errorf("contractID = %q, want %q", got.ContractID, contractID)
	}

	tree, err := db.ListVerificationSourceFiles(ctx, wasmHash)
	if err != nil {
		t.Fatalf("ListVerificationSourceFiles failed: %v", err)
	}
	if len(tree) != 2 {
		t.Fatalf("expected 2 files, got %d", len(tree))
	}

	f, err := db.GetVerificationSourceFile(ctx, wasmHash, "src/lib.rs")
	if err != nil {
		t.Fatalf("GetVerificationSourceFile failed: %v", err)
	}
	if f == nil || f.Content != "fn main() {}" {
		t.Fatalf("unexpected source file content: %+v", f)
	}

	if _, err := db.db.ExecContext(ctx,
		"UPDATE contract_verifications SET status = $1 WHERE id = $2", VerificationStatusVerified, id); err != nil {
		t.Fatalf("failed to mark verification verified: %v", err)
	}

	spamID, err := db.CreateVerification(ctx, v, []VerificationSourceFile{
		{FilePath: "src/lib.rs", Content: "fn spam() {}", SizeBytes: 12},
	})
	if err != nil {
		t.Fatalf("CreateVerification (spam resubmission) failed: %v", err)
	}
	if spamID == id {
		t.Fatal("expected a distinct id for the resubmission")
	}

	got, err = db.GetLatestVerificationByWasmHash(ctx, wasmHash)
	if err != nil {
		t.Fatalf("GetLatestVerificationByWasmHash failed: %v", err)
	}
	if got == nil || got.ID != id {
		t.Fatalf("expected the verified record (id=%d) to stay latest despite a newer pending resubmission, got %+v", id, got)
	}
	if got.Status != VerificationStatusVerified {
		t.Errorf("status = %q, want verified", got.Status)
	}
}
