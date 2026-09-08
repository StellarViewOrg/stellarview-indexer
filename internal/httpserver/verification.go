package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/store"
)

// Verification limits bound the abuse surface of a submission endpoint that
// otherwise has no reason to reject a request: without them a caller could
// submit an unbounded number of files or file sizes and exhaust storage
// before any build ever runs. These are deliberately conservative for real
// Soroban contract source trees.
const (
	maxVerificationFiles      = 500
	maxVerificationFileBytes  = 1 << 20  // 1 MiB per file
	maxVerificationTotalBytes = 10 << 20 // 10 MiB per submission
)

// Metadata field length limits, matching the column widths in
// migrations/000016_create_contract_verifications.up.sql. Checking these
// before INSERT lets an oversized field fail as a 400 instead of surfacing
// as a generic 500 from a truncated Postgres write.
const (
	maxContractIDLen        = 56
	maxNetworkLen           = 16
	maxRepositoryURLLen     = 1024
	maxGitRefLen            = 256
	maxGitCommitLen         = 64
	maxRustVersionLen       = 64
	maxSorobanSDKVersionLen = 64
	maxSourceFilePathLen    = 1024
)

// VerificationStore is the store subset the verification API needs.
type VerificationStore interface {
	GetContractWasmHash(ctx context.Context, contractID string) (string, error)
	ContractCodeExists(ctx context.Context, wasmHash string) (bool, error)
	CreateVerification(ctx context.Context, v *store.ContractVerification, files []store.VerificationSourceFile) (int64, error)
	GetLatestVerificationByWasmHash(ctx context.Context, wasmHash string) (*store.ContractVerification, error)
	GetVerificationByID(ctx context.Context, id int64) (*store.ContractVerification, error)
	ListVerificationSourceFiles(ctx context.Context, wasmHash string) ([]store.VerificationSourceFile, error)
	GetVerificationSourceFile(ctx context.Context, wasmHash, filePath string) (*store.VerificationSourceFile, error)
}

// verifySubmission is the frozen request shape for POST /v1/verify.
//
// This is intentionally the stable, published contract described in issue
// #36: submitting against it always succeeds (subject to validation) and
// records a "pending" verification row. The sandboxed reproducible-build
// pipeline that actually compiles the source and compares the resulting
// wasm hash against contract_code.wasm_hash is tracked as follow-up work;
// until it lands, submissions stay in "pending" and callers should treat
// that as "not yet verified", not as a definite mismatch.
type verifySubmission struct {
	ContractID        string            `json:"contractId"`
	Network           string            `json:"network"`
	RepositoryURL     string            `json:"repositoryUrl,omitempty"`
	GitRef            string            `json:"gitRef,omitempty"`
	GitCommit         string            `json:"gitCommit,omitempty"`
	RustVersion       string            `json:"rustVersion,omitempty"`
	SorobanSDKVersion string            `json:"sorobanSdkVersion,omitempty"`
	BuildProfile      json.RawMessage   `json:"buildProfile,omitempty"`
	Files             map[string]string `json:"files"` // path -> source content
}

type verificationRecord struct {
	ID                int64  `json:"id"`
	WasmHash          string `json:"wasmHash"`
	ContractID        string `json:"contractId"`
	Network           string `json:"network"`
	RepositoryURL     string `json:"repositoryUrl,omitempty"`
	GitRef            string `json:"gitRef,omitempty"`
	GitCommit         string `json:"gitCommit,omitempty"`
	RustVersion       string `json:"rustVersion,omitempty"`
	SorobanSDKVersion string `json:"sorobanSdkVersion,omitempty"`
	Status            string `json:"status"`
	ComputedWasmHash  string `json:"computedWasmHash,omitempty"`
	FailureReason     string `json:"failureReason,omitempty"`
	SubmittedAt       string `json:"submittedAt"`
	CompletedAt       string `json:"completedAt,omitempty"`
}

func toVerificationRecord(v store.ContractVerification) verificationRecord {
	r := verificationRecord{
		ID:          v.ID,
		WasmHash:    v.WasmHash,
		ContractID:  v.ContractID,
		Network:     v.Network,
		Status:      v.Status,
		SubmittedAt: v.SubmittedAt.UTC().Format(rfc3339Milli),
	}
	if v.RepositoryURL != nil {
		r.RepositoryURL = *v.RepositoryURL
	}
	if v.GitRef != nil {
		r.GitRef = *v.GitRef
	}
	if v.GitCommit != nil {
		r.GitCommit = *v.GitCommit
	}
	if v.RustVersion != nil {
		r.RustVersion = *v.RustVersion
	}
	if v.SorobanSDKVersion != nil {
		r.SorobanSDKVersion = *v.SorobanSDKVersion
	}
	if v.ComputedWasmHash != nil {
		r.ComputedWasmHash = *v.ComputedWasmHash
	}
	if v.FailureReason != nil {
		r.FailureReason = *v.FailureReason
	}
	if v.CompletedAt != nil {
		r.CompletedAt = v.CompletedAt.UTC().Format(rfc3339Milli)
	}
	return r
}

const rfc3339Milli = "2006-01-02T15:04:05.000Z07:00"

type sourceFileMeta struct {
	Path  string `json:"path"`
	Bytes int32  `json:"bytes"`
}

// SetVerificationStore attaches the store backing the verification API.
// When unset, verification endpoints return HTTP 503.
func (s *Server) SetVerificationStore(v VerificationStore) {
	s.verification = v
}

func (s *Server) handleVerifySubmit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.verification == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "verification storage is not configured")
		return
	}

	var req verifySubmission
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxVerificationTotalBytes+(1<<20)))
	if err := dec.Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	files, errMsg := normalizeSubmittedFiles(req.Files)
	if errMsg != "" {
		writeJSONError(w, http.StatusBadRequest, errMsg)
		return
	}
	if req.ContractID == "" {
		writeJSONError(w, http.StatusBadRequest, "contractId is required")
		return
	}
	if req.Network == "" {
		writeJSONError(w, http.StatusBadRequest, "network is required")
		return
	}
	if errMsg := validateSubmissionFieldLengths(req); errMsg != "" {
		writeJSONError(w, http.StatusBadRequest, errMsg)
		return
	}

	ctx := r.Context()
	wasmHash, err := s.verification.GetContractWasmHash(ctx, req.ContractID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to resolve contract")
		return
	}
	if wasmHash == "" {
		writeJSONError(w, http.StatusNotFound, "unknown contractId: no indexed wasm_hash for this contract")
		return
	}
	exists, err := s.verification.ContractCodeExists(ctx, wasmHash)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to look up contract code")
		return
	}
	if !exists {
		writeJSONError(w, http.StatusNotFound, "no indexed contract_code for this contract's wasm_hash")
		return
	}

	v := &store.ContractVerification{
		WasmHash:   wasmHash,
		ContractID: req.ContractID,
		Network:    req.Network,
	}
	if req.RepositoryURL != "" {
		v.RepositoryURL = &req.RepositoryURL
	}
	if req.GitRef != "" {
		v.GitRef = &req.GitRef
	}
	if req.GitCommit != "" {
		v.GitCommit = &req.GitCommit
	}
	if req.RustVersion != "" {
		v.RustVersion = &req.RustVersion
	}
	if req.SorobanSDKVersion != "" {
		v.SorobanSDKVersion = &req.SorobanSDKVersion
	}
	if len(req.BuildProfile) > 0 {
		profile := string(req.BuildProfile)
		v.BuildProfile = &profile
	}

	id, err := s.verification.CreateVerification(ctx, v, files)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to record verification submission")
		return
	}

	created, err := s.verification.GetVerificationByID(ctx, id)
	if err != nil || created == nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load submitted verification")
		return
	}
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(toVerificationRecord(*created))
}

func (s *Server) handleVerifyByContract(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.verification == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "verification storage is not configured")
		return
	}
	contractID := r.PathValue("contractId")
	ctx := r.Context()

	wasmHash, err := s.verification.GetContractWasmHash(ctx, contractID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to resolve contract")
		return
	}
	if wasmHash == "" {
		writeJSONError(w, http.StatusNotFound, "unknown contractId")
		return
	}

	v, err := s.verification.GetLatestVerificationByWasmHash(ctx, wasmHash)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load verification")
		return
	}
	if v == nil {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"wasmHash": wasmHash,
			"verified": false,
			"status":   "unverified",
		})
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(toVerificationRecord(*v))
}

func (s *Server) handleVerifyByWasmHash(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.verification == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "verification storage is not configured")
		return
	}
	wasmHash := strings.ToLower(r.PathValue("wasmHash"))

	v, err := s.verification.GetLatestVerificationByWasmHash(r.Context(), wasmHash)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load verification")
		return
	}
	if v == nil {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"wasmHash": wasmHash,
			"verified": false,
			"status":   "unverified",
		})
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(toVerificationRecord(*v))
}

func (s *Server) handleVerifySourceTree(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.verification == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "verification storage is not configured")
		return
	}
	wasmHash := strings.ToLower(r.PathValue("wasmHash"))

	files, err := s.verification.ListVerificationSourceFiles(r.Context(), wasmHash)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list source files")
		return
	}
	out := make([]sourceFileMeta, 0, len(files))
	for _, f := range files {
		out = append(out, sourceFileMeta{Path: f.FilePath, Bytes: f.SizeBytes})
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"wasmHash": wasmHash,
		"files":    out,
	})
}

func (s *Server) handleVerifySourceFile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.verification == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "verification storage is not configured")
		return
	}
	wasmHash := strings.ToLower(r.PathValue("wasmHash"))
	filePath := r.PathValue("path")

	f, err := s.verification.GetVerificationSourceFile(r.Context(), wasmHash, filePath)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load source file")
		return
	}
	if f == nil {
		writeJSONError(w, http.StatusNotFound, "file not found")
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"wasmHash": wasmHash,
		"path":     f.FilePath,
		"content":  f.Content,
		"bytes":    f.SizeBytes,
	})
}

// normalizeSubmittedFiles validates a submission's file map against the
// resource and path-traversal guards described in issue #36 (zip bombs,
// path traversal, resource exhaustion), returning a store-ready slice or a
// human-readable validation error.
func normalizeSubmittedFiles(files map[string]string) ([]store.VerificationSourceFile, string) {
	if len(files) == 0 {
		return nil, "at least one source file is required"
	}
	if len(files) > maxVerificationFiles {
		return nil, fmt.Sprintf("too many files: %d exceeds limit of %d", len(files), maxVerificationFiles)
	}

	out := make([]store.VerificationSourceFile, 0, len(files))
	var total int
	for p, content := range files {
		clean, ok := sanitizeSourcePath(p)
		if !ok {
			return nil, fmt.Sprintf("invalid file path: %q", p)
		}
		size := len(content)
		if size > maxVerificationFileBytes {
			return nil, fmt.Sprintf("file %q exceeds per-file limit of %d bytes", clean, maxVerificationFileBytes)
		}
		total += size
		if total > maxVerificationTotalBytes {
			return nil, fmt.Sprintf("submission exceeds total size limit of %d bytes", maxVerificationTotalBytes)
		}
		out = append(out, store.VerificationSourceFile{
			FilePath:  clean,
			Content:   content,
			SizeBytes: int32(size),
		})
	}
	return out, ""
}

// sanitizeSourcePath rejects absolute paths, empty paths, Windows-drive-style
// paths (`C:/foo`), and any path that escapes its own tree (".." segments),
// which is the same class of traversal a submitted archive could otherwise
// use to write outside its scratch directory once the build pipeline starts
// extracting these paths to disk.
func sanitizeSourcePath(p string) (string, bool) {
	if p == "" || len(p) > maxSourceFilePathLen || strings.ContainsRune(p, 0) {
		return "", false
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") {
		return "", false
	}
	if hasWindowsDrivePrefix(p) {
		return "", false
	}
	cleaned := path.Clean(strings.ReplaceAll(p, "\\", "/"))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", false
	}
	return cleaned, true
}

// hasWindowsDrivePrefix reports whether p starts with a drive letter
// (`C:`), which path.Clean does not treat as absolute since it only knows
// POSIX-style paths.
func hasWindowsDrivePrefix(p string) bool {
	if len(p) < 2 || p[1] != ':' {
		return false
	}
	c := p[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// validateSubmissionFieldLengths checks metadata fields against the column
// widths they will be inserted into, so an oversized field is rejected here
// with a 400 rather than failing INSERT with a generic 500.
func validateSubmissionFieldLengths(req verifySubmission) string {
	switch {
	case len(req.ContractID) > maxContractIDLen:
		return fmt.Sprintf("contractId exceeds max length of %d", maxContractIDLen)
	case len(req.Network) > maxNetworkLen:
		return fmt.Sprintf("network exceeds max length of %d", maxNetworkLen)
	case len(req.RepositoryURL) > maxRepositoryURLLen:
		return fmt.Sprintf("repositoryUrl exceeds max length of %d", maxRepositoryURLLen)
	case len(req.GitRef) > maxGitRefLen:
		return fmt.Sprintf("gitRef exceeds max length of %d", maxGitRefLen)
	case len(req.GitCommit) > maxGitCommitLen:
		return fmt.Sprintf("gitCommit exceeds max length of %d", maxGitCommitLen)
	case len(req.RustVersion) > maxRustVersionLen:
		return fmt.Sprintf("rustVersion exceeds max length of %d", maxRustVersionLen)
	case len(req.SorobanSDKVersion) > maxSorobanSDKVersionLen:
		return fmt.Sprintf("sorobanSdkVersion exceeds max length of %d", maxSorobanSDKVersionLen)
	default:
		return ""
	}
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
