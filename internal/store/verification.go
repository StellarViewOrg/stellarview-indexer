package store

import (
	"context"
	"database/sql"
	"fmt"
)

// GetContractWasmHash resolves a contract_id to the wasm_hash of the code it
// currently runs, so a verification submission by contract_id can be filed
// under the wasm_hash it actually corresponds to.
func (s *PostgresStore) GetContractWasmHash(ctx context.Context, contractID string) (string, error) {
	var wasmHash sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT wasm_hash FROM contracts WHERE contract_id = $1`, contractID).Scan(&wasmHash)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return wasmHash.String, nil
}

// ContractCodeExists reports whether the indexer has already observed and
// stored the given wasm_hash. A verification submission can only be filed
// against bytecode the indexer has actually seen on-chain.
func (s *PostgresStore) ContractCodeExists(ctx context.Context, wasmHash string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM contract_code WHERE wasm_hash = $1)`, wasmHash).Scan(&exists)
	return exists, err
}

// CreateVerification records a new verification submission and its source
// tree in a single transaction. It leaves status as "pending": the build
// pipeline that reproduces the build and compares hashes is a separate,
// not-yet-implemented step (see issue #36) that will later transition the
// record to verified/mismatch/failed via UpdateVerificationResult.
func (s *PostgresStore) CreateVerification(ctx context.Context, v *ContractVerification, files []VerificationSourceFile) (int64, error) {
	dbTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer dbTx.Rollback()

	var id int64
	err = dbTx.QueryRowContext(ctx, `
		INSERT INTO contract_verifications (
			wasm_hash, contract_id, network, repository_url, git_ref, git_commit,
			rust_version, soroban_sdk_version, build_profile, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		RETURNING id`,
		v.WasmHash, v.ContractID, v.Network, v.RepositoryURL, v.GitRef, v.GitCommit,
		v.RustVersion, v.SorobanSDKVersion, v.BuildProfile, VerificationStatusPending,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert contract_verifications: %w", err)
	}

	for _, f := range files {
		if _, err := dbTx.ExecContext(ctx, `
			INSERT INTO contract_verification_sources (verification_id, wasm_hash, file_path, content, size_bytes)
			VALUES ($1,$2,$3,$4,$5)`,
			id, v.WasmHash, f.FilePath, f.Content, f.SizeBytes,
		); err != nil {
			return 0, fmt.Errorf("insert source file %q: %w", f.FilePath, err)
		}
	}

	if err := dbTx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

const verificationSelectCols = `
	id, wasm_hash, contract_id, network,
	repository_url, git_ref, git_commit, rust_version, soroban_sdk_version,
	build_profile::text, status, computed_wasm_hash, failure_reason, build_log,
	submitted_at, completed_at, updated_at`

func scanVerification(row interface {
	Scan(dest ...interface{}) error
}) (*ContractVerification, error) {
	var v ContractVerification
	err := row.Scan(
		&v.ID, &v.WasmHash, &v.ContractID, &v.Network,
		&v.RepositoryURL, &v.GitRef, &v.GitCommit, &v.RustVersion, &v.SorobanSDKVersion,
		&v.BuildProfile, &v.Status, &v.ComputedWasmHash, &v.FailureReason, &v.BuildLog,
		&v.SubmittedAt, &v.CompletedAt, &v.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// latestVerificationOrder ranks a verified submission ahead of every other
// status regardless of recency, falling back to submission recency within
// that. POST /v1/verify has no auth and no check that the submitter is the
// contract's deployer, so without this a garbage resubmission against an
// already-verified wasm_hash would otherwise become "latest" and bury the
// verified record behind it.
const latestVerificationOrder = `ORDER BY (status = 'verified') DESC, submitted_at DESC`

// GetLatestVerificationByWasmHash returns the verified submission for a
// wasm_hash if one exists, otherwise the most recent submission of any
// status, or nil if none has been submitted. See latestVerificationOrder.
func (s *PostgresStore) GetLatestVerificationByWasmHash(ctx context.Context, wasmHash string) (*ContractVerification, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT `+verificationSelectCols+`
		FROM contract_verifications
		WHERE wasm_hash = $1
		`+latestVerificationOrder+`
		LIMIT 1`, wasmHash)
	v, err := scanVerification(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return v, err
}

// GetVerificationByID returns a single verification submission by its id.
func (s *PostgresStore) GetVerificationByID(ctx context.Context, id int64) (*ContractVerification, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT `+verificationSelectCols+`
		FROM contract_verifications
		WHERE id = $1`, id)
	v, err := scanVerification(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return v, err
}

// ListVerificationSourceFiles returns the file tree (path and size, without
// content) belonging to the wasm_hash's latest submission (see
// latestVerificationOrder), so callers can render a source browser's file
// listing cheaply. Scoping to that submission's verification_id (rather than
// wasm_hash alone) ensures an older, already-completed verification's file
// snapshot is never mixed with a different submission's files.
func (s *PostgresStore) ListVerificationSourceFiles(ctx context.Context, wasmHash string) ([]VerificationSourceFile, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT cvs.verification_id, cvs.wasm_hash, cvs.file_path, '', cvs.size_bytes, cvs.created_at
		FROM contract_verification_sources cvs
		WHERE cvs.verification_id = (
			SELECT id FROM contract_verifications
			WHERE wasm_hash = $1
			`+latestVerificationOrder+`
			LIMIT 1
		)
		ORDER BY cvs.file_path`, wasmHash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []VerificationSourceFile
	for rows.Next() {
		var f VerificationSourceFile
		if err := rows.Scan(&f.VerificationID, &f.WasmHash, &f.FilePath, &f.Content, &f.SizeBytes, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetVerificationSourceFile returns one file's content from the wasm_hash's
// latest submission (see latestVerificationOrder), or nil if there is no
// verification for the wasm_hash or the path was not part of that
// submission. See ListVerificationSourceFiles for why this is scoped to the
// latest verification_id rather than wasm_hash alone.
func (s *PostgresStore) GetVerificationSourceFile(ctx context.Context, wasmHash, path string) (*VerificationSourceFile, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT cvs.verification_id, cvs.wasm_hash, cvs.file_path, cvs.content, cvs.size_bytes, cvs.created_at
		FROM contract_verification_sources cvs
		WHERE cvs.file_path = $2
		AND cvs.verification_id = (
			SELECT id FROM contract_verifications
			WHERE wasm_hash = $1
			`+latestVerificationOrder+`
			LIMIT 1
		)`, wasmHash, path)
	var f VerificationSourceFile
	err := row.Scan(&f.VerificationID, &f.WasmHash, &f.FilePath, &f.Content, &f.SizeBytes, &f.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// GetVerificationSourceFilesByVerificationID returns the exact file snapshot
// that produced a specific verification record's result, independent of
// whatever the wasm_hash's latest submission happens to be.
func (s *PostgresStore) GetVerificationSourceFilesByVerificationID(ctx context.Context, verificationID int64) ([]VerificationSourceFile, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT verification_id, wasm_hash, file_path, content, size_bytes, created_at
		FROM contract_verification_sources
		WHERE verification_id = $1
		ORDER BY file_path`, verificationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []VerificationSourceFile
	for rows.Next() {
		var f VerificationSourceFile
		if err := rows.Scan(&f.VerificationID, &f.WasmHash, &f.FilePath, &f.Content, &f.SizeBytes, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
