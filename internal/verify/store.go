package verify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

type Completion struct {
	ID         string
	Status     Status
	Reason     string
	Match      bool
	WasmHash   string
	Network    string
	Toolchain  *Toolchain
	Logs       string
	DurationMS int64
	Files      []SourceFile
}

type Store interface {
	CreateVerification(ctx context.Context, v *Verification) error
	SetBuilding(ctx context.Context, id string) error
	Complete(ctx context.Context, c *Completion) error
	GetVerification(ctx context.Context, id string) (*Verification, error)
	GetLogs(ctx context.Context, id string) (string, error)
	GetVerifiedSource(ctx context.Context, wasmHash string) (*VerifiedSource, error)
	ListSourceFiles(ctx context.Context, wasmHash string) ([]SourceFileMeta, error)
	GetSourceFile(ctx context.Context, wasmHash, path string) (*SourceFile, error)
	ExpectedWasmHash(ctx context.Context, contractID string) (*string, error)
	FailInterrupted(ctx context.Context) (int64, error)
	Close() error
}

type PGStore struct {
	db *sql.DB
}

func NewPGStore(db *sql.DB) *PGStore {
	return &PGStore{db: db}
}

func marshalJSON(v interface{}) *string {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	s := string(b)
	return &s
}

func unmarshalJSONPtr(s sql.NullString, out interface{}) {
	if !s.Valid || s.String == "" {
		return
	}
	_ = json.Unmarshal([]byte(s.String), out)
}

func (s *PGStore) CreateVerification(ctx context.Context, v *Verification) error {
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO contract_verifications (
			id, contract_id, network, status,
			expected_wasm_hash,
			source_type, archive_name, archive_sha256,
			git_repository, git_reference, git_commit,
			toolchain_requested, build_spec, submitter_ip, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		v.ID, v.ContractID, v.Network, string(v.Status),
		v.ExpectedWasmHash,
		v.Source.Type, nullableString(v.Source.Name), nullableString(v.Source.SHA256),
		nullableString(v.Source.Repository), nullableString(v.Source.Reference), nullableString(v.Source.Commit),
		marshalJSON(v.ToolchainRequested), marshalJSON(v.Build), nullableString(v.SubmitterIP),
		v.CreatedAt,
	)
	return err
}

func (s *PGStore) SetBuilding(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE contract_verifications
		SET status = $2, started_at = NOW()
		WHERE id = $1 AND status = 'queued'`,
		id, string(StatusBuilding))
	return err
}

func (s *PGStore) Complete(ctx context.Context, c *Completion) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		UPDATE contract_verifications
		SET status = $2,
		    reason = NULLIF($3, ''),
		    match = $4,
		    wasm_hash = NULLIF($5, ''),
		    toolchain_actual = $6,
		    build_logs = NULLIF($7, ''),
		    build_duration_ms = $8,
		    completed_at = NOW()
		WHERE id = $1`,
		c.ID, string(c.Status), c.Reason, c.Match, c.WasmHash,
		marshalJSON(c.Toolchain), c.Logs, c.DurationMS,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("verification %s not found", c.ID)
	}

	if c.Status == StatusVerified {
		var won bool
		err := tx.QueryRowContext(ctx, `
			INSERT INTO verified_sources (wasm_hash, verification_id, contract_id, network, file_count, total_size)
			SELECT $1, id, contract_id, network, $3, $4
			FROM contract_verifications WHERE id = $2
			ON CONFLICT (wasm_hash) DO NOTHING
			RETURNING wasm_hash`,
			c.WasmHash, c.ID, len(c.Files), totalFileSize(c.Files),
		).Scan(new(string))
		switch {
		case err == nil:
			won = true
		case errors.Is(err, sql.ErrNoRows):
			won = false
		default:
			return err
		}

		if won && len(c.Files) > 0 {
			stmt, err := tx.PrepareContext(ctx, `
				INSERT INTO source_files (wasm_hash, path, size, content)
				VALUES ($1,$2,$3,$4)
				ON CONFLICT (wasm_hash, path) DO NOTHING`)
			if err != nil {
				return err
			}
			defer stmt.Close()
			for _, f := range c.Files {
				if _, err := stmt.ExecContext(ctx, c.WasmHash, f.Path, len(f.Content), f.Content); err != nil {
					return err
				}
			}
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE contract_code SET verified = TRUE WHERE wasm_hash = $1`, c.WasmHash); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func totalFileSize(files []SourceFile) int64 {
	var n int64
	for _, f := range files {
		n += int64(len(f.Content))
	}
	return n
}

func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

const verificationColumns = `
	id, contract_id, network, status, COALESCE(reason, ''), match,
	wasm_hash, expected_wasm_hash,
	source_type, COALESCE(archive_name, ''), COALESCE(archive_sha256, ''),
	COALESCE(git_repository, ''), COALESCE(git_reference, ''), COALESCE(git_commit, ''),
	toolchain_requested, toolchain_actual, build_spec,
	build_duration_ms, submitter_ip, created_at, started_at, completed_at`

func scanVerification(row interface{ Scan(...interface{}) error }) (*Verification, error) {
	var (
		v                  Verification
		status             string
		match              sql.NullBool
		wasmHash           sql.NullString
		expectedWasmHash   sql.NullString
		toolchainRequested sql.NullString
		toolchainActual    sql.NullString
		buildSpec          sql.NullString
		durationMS         sql.NullInt64
		submitterIP        sql.NullString
		startedAt          sql.NullTime
		completedAt        sql.NullTime
	)
	if err := row.Scan(
		&v.ID, &v.ContractID, &v.Network, &status, &v.Reason, &match,
		&wasmHash, &expectedWasmHash,
		&v.Source.Type, &v.Source.Name, &v.Source.SHA256,
		&v.Source.Repository, &v.Source.Reference, &v.Source.Commit,
		&toolchainRequested, &toolchainActual, &buildSpec,
		&durationMS, &submitterIP, &v.CreatedAt, &startedAt, &completedAt,
	); err != nil {
		return nil, err
	}
	v.Status = Status(status)
	if match.Valid {
		b := match.Bool
		v.Match = &b
	}
	if wasmHash.Valid {
		h := wasmHash.String
		v.WasmHash = &h
	}
	if expectedWasmHash.Valid {
		h := expectedWasmHash.String
		v.ExpectedWasmHash = &h
	}
	unmarshalJSONPtr(toolchainRequested, &v.ToolchainRequested)
	if toolchainActual.Valid {
		tc := &Toolchain{}
		unmarshalJSONPtr(toolchainActual, tc)
		v.ToolchainActual = tc
	}
	unmarshalJSONPtr(buildSpec, &v.Build)
	if durationMS.Valid {
		d := durationMS.Int64
		v.BuildDurationMS = &d
	}
	v.SubmitterIP = submitterIP.String
	if startedAt.Valid {
		t := startedAt.Time
		v.StartedAt = &t
	}
	if completedAt.Valid {
		t := completedAt.Time
		v.CompletedAt = &t
	}
	return &v, nil
}

func (s *PGStore) GetVerification(ctx context.Context, id string) (*Verification, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+verificationColumns+` FROM contract_verifications WHERE id = $1`, id)
	v, err := scanVerification(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return v, err
}

func (s *PGStore) GetLogs(ctx context.Context, id string) (string, error) {
	var logs sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT build_logs FROM contract_verifications WHERE id = $1`, id).Scan(&logs)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return logs.String, nil
}

func (s *PGStore) GetVerifiedSource(ctx context.Context, wasmHash string) (*VerifiedSource, error) {
	var (
		vs              VerifiedSource
		toolchainActual sql.NullString
		buildSpec       sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT vs.wasm_hash, vs.verification_id, vs.contract_id, vs.network,
		       vs.file_count, vs.total_size, vs.verified_at,
		       cv.toolchain_actual, cv.build_spec, cv.source_type,
		       COALESCE(cv.archive_name, ''), COALESCE(cv.archive_sha256, ''),
		       COALESCE(cv.git_repository, ''), COALESCE(cv.git_reference, ''), COALESCE(cv.git_commit, '')
		FROM verified_sources vs
		JOIN contract_verifications cv ON cv.id = vs.verification_id
		WHERE vs.wasm_hash = $1`, wasmHash).Scan(
		&vs.WasmHash, &vs.VerificationID, &vs.ContractID, &vs.Network,
		&vs.FileCount, &vs.TotalSize, &vs.VerifiedAt,
		&toolchainActual, &buildSpec, &vs.Source.Type,
		&vs.Source.Name, &vs.Source.SHA256,
		&vs.Source.Repository, &vs.Source.Reference, &vs.Source.Commit,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if toolchainActual.Valid {
		tc := &Toolchain{}
		unmarshalJSONPtr(toolchainActual, tc)
		vs.Toolchain = tc
	}
	if buildSpec.Valid {
		bs := &BuildSpec{}
		unmarshalJSONPtr(buildSpec, bs)
		vs.Build = bs
	}
	return &vs, nil
}

func (s *PGStore) ListSourceFiles(ctx context.Context, wasmHash string) ([]SourceFileMeta, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT path, size FROM source_files WHERE wasm_hash = $1 ORDER BY path`, wasmHash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []SourceFileMeta
	for rows.Next() {
		var f SourceFileMeta
		if err := rows.Scan(&f.Path, &f.Size); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

func (s *PGStore) GetSourceFile(ctx context.Context, wasmHash, path string) (*SourceFile, error) {
	var f SourceFile
	err := s.db.QueryRowContext(ctx,
		`SELECT path, content FROM source_files WHERE wasm_hash = $1 AND path = $2`,
		wasmHash, path).Scan(&f.Path, &f.Content)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (s *PGStore) ExpectedWasmHash(ctx context.Context, contractID string) (*string, error) {
	var h sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT wasm_hash FROM contracts WHERE contract_id = $1`, contractID).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnknownContract
	}
	if err != nil {
		return nil, err
	}
	if !h.Valid {
		return nil, ErrSACUnsupported
	}
	return &h.String, nil
}

func (s *PGStore) FailInterrupted(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE contract_verifications
		SET status = 'failed',
		    reason = 'interrupted by indexer restart; please resubmit',
		    completed_at = NOW()
		WHERE status IN ('queued', 'building') AND created_at < NOW() - INTERVAL '5 minutes'`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *PGStore) Close() error {
	return s.db.Close()
}

var _ Store = (*PGStore)(nil)
