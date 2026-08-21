package verify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxFormMemory = 8 << 20

type Server struct {
	srv     *http.Server
	service *Service
}

func NewServer(addr string, service *Service) *Server {
	s := &Server{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/contracts/{contractID}/verifications", s.handleSubmit)
	mux.HandleFunc("GET /api/v1/verifications/{id}", s.handleStatus)
	mux.HandleFunc("GET /api/v1/verifications/{id}/logs", s.handleLogs)
	mux.HandleFunc("GET /api/v1/wasm/{wasmHash}/verification", s.handleLookup)
	mux.HandleFunc("GET /api/v1/wasm/{wasmHash}/source/tree", s.handleTree)
	mux.HandleFunc("GET /api/v1/wasm/{wasmHash}/source/file", s.handleFile)
	s.srv = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

func (s *Server) Start() error {
	if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

type errDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorBody struct {
	Error errDetail `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorBody{Error: errDetail{Code: code, Message: msg}})
}

func errorCode(err error) (int, string) {
	switch {
	case errors.Is(err, ErrRateLimited):
		return http.StatusTooManyRequests, "rate_limited"
	case errors.Is(err, ErrQueueFull):
		return http.StatusServiceUnavailable, "queue_full"
	case errors.Is(err, ErrUnknownContract):
		return http.StatusNotFound, "unknown_contract"
	case errors.Is(err, ErrSACUnsupported):
		return http.StatusUnprocessableEntity, "sac_unsupported"
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, ErrInvalidRequest):
		return http.StatusBadRequest, "invalid_request"
	default:
		return http.StatusInternalServerError, "internal"
	}
}

func writeApiError(w http.ResponseWriter, err error) {
	status, code := errorCode(err)
	if status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "1")
	}
	msg := err.Error()
	if status == http.StatusInternalServerError {
		msg = "internal error"
	}
	writeError(w, status, code, msg)
}

type submitResponse struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	StatusURL string `json:"status_url"`
	CreatedAt string `json:"created_at"`
}

type jsonSubmitRequest struct {
	Network   string      `json:"network"`
	Source    *SourceSpec `json:"source"`
	Toolchain Toolchain   `json:"toolchain"`
	Build     BuildSpec   `json:"build"`
}

func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}

func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	contractID := r.PathValue("contractID")

	ct := r.Header.Get("Content-Type")
	sub := Submission{ContractID: contractID, IP: clientIP(r)}

	switch {
	case strings.HasPrefix(ct, "application/json"):
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "cannot read request body")
			return
		}
		var req jsonSubmitRequest
		if err := json.Unmarshal(body, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
			return
		}
		sub.Network = req.Network
		sub.Toolchain = req.Toolchain
		sub.Build = req.Build
		if req.Source != nil && req.Source.Type == SourceTypeGit {
			sub.Git = req.Source
		} else {
			writeApiError(w, fmt.Errorf("%w: JSON submissions require source.type = \"git\"; upload archives via multipart/form-data", ErrInvalidRequest))
			return
		}
	case strings.HasPrefix(ct, "multipart/form-data"):
		r.Body = http.MaxBytesReader(w, r.Body, s.service.cfg.MaxArchiveBytes+(4<<20))
		if err := r.ParseMultipartForm(maxFormMemory); err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "invalid_request",
				fmt.Sprintf("upload too large or malformed (max archive %d bytes)", s.service.cfg.MaxArchiveBytes))
			return
		}
		defer func() {
			if r.MultipartForm != nil {
				_ = r.MultipartForm.RemoveAll()
			}
		}()
		file, header, err := r.FormFile("archive")
		if err != nil {
			writeApiError(w, fmt.Errorf("%w: multipart field \"archive\" is required", ErrInvalidRequest))
			return
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "cannot read uploaded archive")
			return
		}
		sub.Archive = data
		if header != nil {
			sub.ArchiveName = header.Filename
		}
		sub.Network = r.FormValue("network")
		sub.Toolchain = Toolchain{
			Rust:       r.FormValue("toolchain_rust"),
			SorobanSDK: r.FormValue("toolchain_soroban_sdk"),
			StellarCLI: r.FormValue("toolchain_stellar_cli"),
		}
		sub.Build = BuildSpec{
			Profile:           r.FormValue("build_profile"),
			Package:           r.FormValue("build_package"),
			CrateDir:          r.FormValue("build_crate_dir"),
			Features:          splitCSV(r.FormValue("build_features")),
			Flags:             splitCSV(r.FormValue("build_flags")),
			NoDefaultFeatures: r.FormValue("build_no_default_features") == "true",
		}
	default:
		writeError(w, http.StatusUnsupportedMediaType, "invalid_request",
			"use multipart/form-data (archive upload) or application/json (git source)")
		return
	}

	v, err := s.service.Submit(r.Context(), sub)
	if err != nil {
		writeApiError(w, err)
		return
	}

	writeJSON(w, http.StatusAccepted, submitResponse{
		ID:        v.ID,
		Status:    string(v.Status),
		StatusURL: "/api/v1/verifications/" + v.ID,
		CreatedAt: v.CreatedAt.Format(time.RFC3339Nano),
	})
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	v, err := s.service.store.GetVerification(r.Context(), r.PathValue("id"))
	if err != nil {
		writeApiError(w, err)
		return
	}
	resp := verificationToResponse(v)
	resp.BuildLogsURL = "/api/v1/verifications/" + v.ID + "/logs"
	writeJSON(w, http.StatusOK, resp)
}

type VerificationResponse struct {
	ID                 string     `json:"id"`
	ContractID         string     `json:"contract_id"`
	Network            string     `json:"network"`
	Status             string     `json:"status"`
	Reason             string     `json:"reason,omitempty"`
	Match              *bool      `json:"match"`
	WasmHash           *string    `json:"wasm_hash"`
	ExpectedWasmHash   *string    `json:"expected_wasm_hash"`
	Source             SourceSpec `json:"source"`
	ToolchainRequested Toolchain  `json:"toolchain_requested"`
	Toolchain          *Toolchain `json:"toolchain"`
	Build              BuildSpec  `json:"build"`
	BuildLogsURL       string     `json:"build_logs_url,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	StartedAt          *time.Time `json:"started_at,omitempty"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
}

func verificationToResponse(v *Verification) VerificationResponse {
	return VerificationResponse{
		ID:                 v.ID,
		ContractID:         v.ContractID,
		Network:            v.Network,
		Status:             string(v.Status),
		Reason:             v.Reason,
		Match:              v.Match,
		WasmHash:           v.WasmHash,
		ExpectedWasmHash:   v.ExpectedWasmHash,
		Source:             v.Source,
		ToolchainRequested: v.ToolchainRequested,
		Toolchain:          v.ToolchainActual,
		Build:              v.Build,
		CreatedAt:          v.CreatedAt,
		StartedAt:          v.StartedAt,
		CompletedAt:        v.CompletedAt,
	}
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	logs, err := s.service.store.GetLogs(r.Context(), r.PathValue("id"))
	if err != nil {
		writeApiError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(logs))
}

type lookupResponse struct {
	WasmHash       string      `json:"wasm_hash"`
	Verified       bool        `json:"verified"`
	VerificationID string      `json:"verification_id,omitempty"`
	ContractID     string      `json:"contract_id,omitempty"`
	Network        string      `json:"network,omitempty"`
	Toolchain      *Toolchain  `json:"toolchain,omitempty"`
	Build          *BuildSpec  `json:"build,omitempty"`
	Source         *SourceSpec `json:"source,omitempty"`
	FileCount      int         `json:"file_count,omitempty"`
	TotalSize      int64       `json:"total_size,omitempty"`
	VerifiedAt     string      `json:"verified_at,omitempty"`
}

var validWasmHashRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validWasmHash(h string) bool {
	return validWasmHashRe.MatchString(h)
}

func (s *Server) handleLookup(w http.ResponseWriter, r *http.Request) {
	hash := strings.ToLower(r.PathValue("wasmHash"))
	if !validWasmHash(hash) {
		writeError(w, http.StatusBadRequest, "invalid_request", "wasm_hash must be 64 lowercase hex characters")
		return
	}
	vs, err := s.service.store.GetVerifiedSource(r.Context(), hash)
	if errors.Is(err, ErrNotFound) {
		writeJSON(w, http.StatusOK, lookupResponse{WasmHash: hash, Verified: false})
		return
	}
	if err != nil {
		writeApiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lookupResponse{
		WasmHash:       vs.WasmHash,
		Verified:       true,
		VerificationID: vs.VerificationID,
		ContractID:     vs.ContractID,
		Network:        vs.Network,
		Toolchain:      vs.Toolchain,
		Build:          vs.Build,
		Source:         vs.Source,
		FileCount:      vs.FileCount,
		TotalSize:      vs.TotalSize,
		VerifiedAt:     vs.VerifiedAt.UTC().Format(time.RFC3339Nano),
	})
}

type treeResponse struct {
	WasmHash string         `json:"wasm_hash"`
	Verified bool           `json:"verified"`
	Files    []treeEntryDTO `json:"files"`
}

type treeEntryDTO struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	hash := strings.ToLower(r.PathValue("wasmHash"))
	if !validWasmHash(hash) {
		writeError(w, http.StatusBadRequest, "invalid_request", "wasm_hash must be 64 lowercase hex characters")
		return
	}
	files, err := s.service.store.ListSourceFiles(r.Context(), hash)
	if err != nil {
		writeApiError(w, err)
		return
	}
	if files == nil {
		writeApiError(w, ErrNotFound)
		return
	}
	entries := make([]treeEntryDTO, 0, len(files))
	for _, f := range files {
		entries = append(entries, treeEntryDTO{Path: f.Path, Size: f.Size})
	}
	writeJSON(w, http.StatusOK, treeResponse{WasmHash: hash, Verified: true, Files: entries})
}

func contentTypeFor(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".rs", ".toml", ".txt", ".md", ".yml", ".yaml", ".lock", ".json", "":
		if strings.HasSuffix(path, ".json") {
			return "application/json"
		}
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	hash := strings.ToLower(r.PathValue("wasmHash"))
	if !validWasmHash(hash) {
		writeError(w, http.StatusBadRequest, "invalid_request", "wasm_hash must be 64 lowercase hex characters")
		return
	}
	p := r.URL.Query().Get("path")
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\x00") {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameter \"path\" is required and must be relative")
		return
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			writeError(w, http.StatusBadRequest, "invalid_request", "path may not traverse upward")
			return
		}
	}
	f, err := s.service.store.GetSourceFile(r.Context(), hash, p)
	if err != nil {
		writeApiError(w, err)
		return
	}
	w.Header().Set("Content-Type", contentTypeFor(f.Path))
	w.Header().Set("X-File-Path", f.Path)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(f.Content)
}
