package verify

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/stellar/go-stellar-sdk/strkey"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/metrics"
)

type EventPublisher interface {
	PublishVerification(ctx context.Context, evt VerificationEvent) error
}

type VerificationEvent struct {
	ID         string  `json:"id"`
	ContractID string  `json:"contract_id"`
	Status     string  `json:"status"`
	Match      *bool   `json:"match"`
	WasmHash   *string `json:"wasm_hash"`
}

type ServiceConfig struct {
	Store            Store
	Builder          Builder
	Publisher        EventPublisher
	Limiter          *IPRateLimiter
	Network          string
	WorkspaceDir     string
	MaxArchiveBytes  int64
	ExtractLimits    ExtractLimits
	QueueSize        int
	BuildConcurrency int
	BuildTimeout     time.Duration
}

type Service struct {
	cfg    ServiceConfig
	store  Store
	jobs   chan *job
	stopCh chan struct{}
	wg     sync.WaitGroup
}

type job struct {
	v       *Verification
	archive []byte
}

type Submission struct {
	ContractID  string
	Network     string
	Archive     []byte
	ArchiveName string
	Git         *SourceSpec
	Toolchain   Toolchain
	Build       BuildSpec
	IP          string
}

func NewService(cfg ServiceConfig) (*Service, error) {
	if cfg.Store == nil || cfg.Builder == nil {
		return nil, fmt.Errorf("service requires a store and a builder")
	}
	if cfg.QueueSize < 1 {
		cfg.QueueSize = 16
	}
	if cfg.BuildConcurrency < 1 {
		cfg.BuildConcurrency = 2
	}
	if cfg.BuildTimeout <= 0 {
		cfg.BuildTimeout = 20 * time.Minute
	}
	if cfg.MaxArchiveBytes <= 0 {
		cfg.MaxArchiveBytes = 20 << 20
	}
	if cfg.ExtractLimits.MaxTotalBytes <= 0 {
		cfg.ExtractLimits = DefaultExtractLimits(100 << 20)
	}
	if cfg.WorkspaceDir == "" {
		cfg.WorkspaceDir = filepath.Join(os.TempDir(), "stellarview-verify")
	}
	if err := os.MkdirAll(cfg.WorkspaceDir, 0o755); err != nil {
		return nil, fmt.Errorf("create workspace dir: %w", err)
	}
	return &Service{
		cfg:    cfg,
		store:  cfg.Store,
		jobs:   make(chan *job, cfg.QueueSize),
		stopCh: make(chan struct{}),
	}, nil
}

func (s *Service) Start(workers int) {
	if workers < 1 {
		workers = s.cfg.BuildConcurrency
	}
	for i := 0; i < workers; i++ {
		s.wg.Add(1)
		go s.worker()
	}
}

func (s *Service) Stop(timeout time.Duration) {
	close(s.stopCh)
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		log.Printf("verify: shutdown timed out with workers still running")
	}
}

func (s *Service) QueueDepth() int { return len(s.jobs) }

var (
	packageNameRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	crateDirRe     = regexp.MustCompile(`^[A-Za-z0-9_./-]{1,256}$`)
	featureRe      = regexp.MustCompile(`^[A-Za-z0-9_+-]{1,64}$`)
	buildFlagRe    = regexp.MustCompile(`^[A-Za-z0-9_=.,:+-]{1,128}$`)
	maxListedItems = 16
)

func validateBuildSpec(b *BuildSpec) error {
	if b.Profile == "" {
		b.Profile = ProfileRelease
	}
	if b.Profile != ProfileRelease {
		return fmt.Errorf("%w: unsupported build profile %q (only \"release\")", ErrInvalidRequest, b.Profile)
	}
	if b.Package != "" && !packageNameRe.MatchString(b.Package) {
		return fmt.Errorf("%w: invalid package name", ErrInvalidRequest)
	}
	if b.CrateDir != "" {
		if strings.HasPrefix(b.CrateDir, "/") || !crateDirRe.MatchString(b.CrateDir) ||
			len(strings.Split(strings.Trim(b.CrateDir, "/"), "/")) > maxListedItems {
			return fmt.Errorf("%w: invalid crate_dir", ErrInvalidRequest)
		}
		for _, seg := range strings.Split(b.CrateDir, "/") {
			if seg == ".." {
				return fmt.Errorf("%w: crate_dir may not traverse upward", ErrInvalidRequest)
			}
		}
	}
	if len(b.Features) > maxListedItems || len(b.Flags) > maxListedItems {
		return fmt.Errorf("%w: too many features/flags (max %d)", ErrInvalidRequest, maxListedItems)
	}
	for _, f := range b.Features {
		if !featureRe.MatchString(f) {
			return fmt.Errorf("%w: invalid feature %q", ErrInvalidRequest, f)
		}
	}
	for _, f := range b.Flags {
		if !buildFlagRe.MatchString(f) {
			return fmt.Errorf("%w: invalid build flag %q", ErrInvalidRequest, f)
		}
	}
	return nil
}

func newUUIDv4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func (s *Service) Submit(ctx context.Context, sub Submission) (*Verification, error) {
	if s.cfg.Limiter != nil && !s.cfg.Limiter.Allow(sub.IP) {
		metrics.VerificationSubmissions.WithLabelValues("rejected").Inc()
		return nil, ErrRateLimited
	}

	if _, err := strkey.Decode(strkey.VersionByteContract, sub.ContractID); err != nil {
		metrics.VerificationSubmissions.WithLabelValues("rejected").Inc()
		return nil, fmt.Errorf("%w: contract_id must be a valid Stellar contract address", ErrInvalidRequest)
	}
	network := sub.Network
	if network == "" {
		network = s.cfg.Network
	}
	if network != s.cfg.Network {
		metrics.VerificationSubmissions.WithLabelValues("rejected").Inc()
		return nil, fmt.Errorf("%w: network %q does not match this indexer's network %q",
			ErrInvalidRequest, network, s.cfg.Network)
	}
	if err := validateBuildSpec(&sub.Build); err != nil {
		metrics.VerificationSubmissions.WithLabelValues("rejected").Inc()
		return nil, err
	}

	src := SourceSpec{Type: SourceTypeArchive}
	switch {
	case sub.Git != nil:
		if err := ValidateGitSource(*sub.Git); err != nil {
			metrics.VerificationSubmissions.WithLabelValues("rejected").Inc()
			return nil, err
		}
		src = SourceSpec{
			Type:       SourceTypeGit,
			Repository: sub.Git.Repository,
			Reference:  sub.Git.Reference,
			Commit:     strings.ToLower(sub.Git.Commit),
		}
	case len(sub.Archive) > 0:
		if int64(len(sub.Archive)) > s.cfg.MaxArchiveBytes {
			metrics.VerificationSubmissions.WithLabelValues("rejected").Inc()
			return nil, fmt.Errorf("%w: archive exceeds maximum size of %d bytes", ErrInvalidRequest, s.cfg.MaxArchiveBytes)
		}
		sum := sha256.Sum256(sub.Archive)
		src.SHA256 = hex.EncodeToString(sum[:])
		name := filepath.Base(filepath.FromSlash(strings.ReplaceAll(sub.ArchiveName, "\\", "/")))
		if len(name) > 256 {
			name = name[len(name)-256:]
		}
		src.Name = name
	default:
		metrics.VerificationSubmissions.WithLabelValues("rejected").Inc()
		return nil, fmt.Errorf("%w: no source provided (upload an archive or specify a git source)", ErrInvalidRequest)
	}

	expected, err := s.store.ExpectedWasmHash(ctx, sub.ContractID)
	if err != nil {
		metrics.VerificationSubmissions.WithLabelValues("rejected").Inc()
		return nil, err
	}

	v := &Verification{
		ID:                 newUUIDv4(),
		ContractID:         sub.ContractID,
		Network:            network,
		Status:             StatusQueued,
		ExpectedWasmHash:   expected,
		Source:             src,
		ToolchainRequested: sub.Toolchain,
		Build:              sub.Build,
		SubmitterIP:        sub.IP,
		CreatedAt:          time.Now().UTC(),
	}
	if err := s.store.CreateVerification(ctx, v); err != nil {
		metrics.VerificationSubmissions.WithLabelValues("rejected").Inc()
		return nil, fmt.Errorf("persist verification: %w", err)
	}

	select {
	case s.jobs <- &job{v: v, archive: sub.Archive}:
	default:
		_, _ = s.store.Complete(ctx, &Completion{
			ID: v.ID, Status: StatusFailed,
			Reason: "verification queue is full; please resubmit",
		})
		metrics.VerificationSubmissions.WithLabelValues("rejected").Inc()
		return nil, ErrQueueFull
	}

	metrics.VerificationSubmissions.WithLabelValues("accepted").Inc()
	return v, nil
}

func (s *Service) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.stopCh:
			return
		case j := <-s.jobs:
			if j == nil {
				return
			}
			s.processJob(context.Background(), j)
		}
	}
}

func (s *Service) processJob(ctx context.Context, j *job) {
	v := j.v
	metrics.VerificationQueueLength.Set(float64(len(s.jobs)))

	jobCtx, cancel := context.WithTimeout(ctx, s.cfg.BuildTimeout)
	defer cancel()

	started := time.Now()
	if err := s.store.SetBuilding(jobCtx, v.ID); err != nil {
		log.Printf("verify: mark building %s: %v", v.ID, err)
	}

	completion := s.runVerification(jobCtx, j)
	completion.DurationMS = time.Since(started).Milliseconds()

	if err := s.store.Complete(jobCtx, completion); err != nil {
		log.Printf("verify: persist completion for %s: %v", v.ID, err)
	}

	wasmHash := completion.WasmHash
	match := completion.Match
	evt := VerificationEvent{
		ID:         v.ID,
		ContractID: v.ContractID,
		Status:     string(completion.Status),
		Match:      &match,
	}
	if wasmHash != "" {
		h := wasmHash
		evt.WasmHash = &h
	}
	if s.cfg.Publisher != nil {
		if err := s.cfg.Publisher.PublishVerification(ctx, evt); err != nil {
			log.Printf("verify: publish event for %s: %v", v.ID, err)
		}
	}
	metrics.VerificationBuilds.WithLabelValues(string(completion.Status)).Inc()
	metrics.VerificationBuildDuration.Observe(time.Since(started).Seconds())
}

func (s *Service) runVerification(ctx context.Context, j *job) *Completion {
	v := j.v

	workDir := filepath.Join(s.cfg.WorkspaceDir, "job-"+v.ID)
	srcDir := filepath.Join(workDir, "src")
	outDir := filepath.Join(workDir, "out")
	defer os.RemoveAll(workDir)

	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		return failed(v, "internal error preparing workspace")
	}
	if err := os.MkdirAll(outDir, 0o777); err != nil {
		return failed(v, "internal error preparing workspace")
	}

	switch v.Source.Type {
	case SourceTypeGit:
		if err := fetchGitSourceFunc(ctx, v.Source, srcDir); err != nil {
			return failed(v, fmt.Sprintf("fetching git source: %v", err))
		}
	case SourceTypeArchive:
		if _, err := ExtractArchive(j.archive, srcDir, s.cfg.ExtractLimits); err != nil {
			return failed(v, sanitizeExtractError(err))
		}
	default:
		return failed(v, "unknown source type")
	}

	crateDir := srcDir
	if v.Build.CrateDir != "" {
		candidate := filepath.Join(srcDir, filepath.FromSlash(relClean(v.Build.CrateDir)))
		if _, err := os.Stat(filepath.Join(candidate, "Cargo.toml")); err != nil {
			return failed(v, fmt.Sprintf("crate_dir %q does not contain a Cargo.toml", v.Build.CrateDir))
		}
		crateDir = candidate
	} else if _, err := os.Stat(filepath.Join(srcDir, "Cargo.toml")); err != nil {
		if found := findCargoToml(srcDir, 3); found != "" {
			crateDir = found
		} else {
			return failed(v, "no Cargo.toml found at the root of the submitted source")
		}
	}

	res, err := s.cfg.Builder.Build(ctx, BuildRequest{
		ID:        v.ID,
		SourceDir: crateDir,
		OutputDir: outDir,
		Spec:      v.Build,
	})
	if err != nil {
		completion := failed(v, err.Error())
		var be *BuildError
		if errors.As(err, &be) && be.Logs != "" {
			completion.Logs = be.Logs
		}
		return completion
	}

	got := sha256.Sum256(res.Wasm)
	gotHex := hex.EncodeToString(got[:])
	expected := ""
	if v.ExpectedWasmHash != nil {
		expected = strings.ToLower(*v.ExpectedWasmHash)
	}
	match := gotHex == expected

	tc := res.Toolchain
	if tc.Rust == "" && tc.StellarCLI == "" && tc.SorobanSDK == "" {
		tc = Toolchain{Rust: v.ToolchainRequested.Rust, SorobanSDK: v.ToolchainRequested.SorobanSDK, StellarCLI: v.ToolchainRequested.StellarCLI}
	}

	if match {
		files, ferr := collectSourceFiles(srcDir, s.cfg.ExtractLimits.MaxFiles)
		if ferr != nil {
			return failed(v, fmt.Sprintf("collecting verified source files: %v", ferr))
		}
		return &Completion{
			ID: v.ID, Status: StatusVerified, Match: true,
			WasmHash: gotHex, Network: v.Network,
			Toolchain: &tc, Logs: res.Logs, Files: files,
		}
	}

	return &Completion{
		ID: v.ID, Status: StatusMismatch, Match: false,
		WasmHash: gotHex, Network: v.Network,
		Reason:    fmt.Sprintf("built WASM hash %s does not match on-chain hash %s", gotHex, expected),
		Toolchain: &tc, Logs: res.Logs,
	}
}

func relClean(p string) string { return filepath.Clean("/" + p)[1:] }

func findCargoToml(root string, maxDepth int) string {
	var found string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || found != "" {
			return filepath.SkipAll
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		if depth := strings.Count(rel, string(os.PathSeparator)); depth > maxDepth {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Name() == "Cargo.toml" {
			found = filepath.Dir(p)
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func collectSourceFiles(root string, maxFiles int) ([]SourceFile, error) {
	var files []SourceFile
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "target" || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		content, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		files = append(files, SourceFile{Path: filepath.ToSlash(rel), Content: content})
		if maxFiles > 0 && len(files) > maxFiles {
			return fmt.Errorf("source tree exceeds %d files", maxFiles)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func failed(v *Verification, reason string) *Completion {
	return &Completion{ID: v.ID, Status: StatusFailed, Reason: truncateReason(reason)}
}

func sanitizeExtractError(err error) string {
	if IsUnsafeArchive(err) {
		return err.Error()
	}
	return "extracting archive: " + err.Error()
}

func truncateReason(s string) string {
	const max = 2000
	runes := []rune(s)
	if len(runes) > max {
		return string(runes[:max])
	}
	return s
}
