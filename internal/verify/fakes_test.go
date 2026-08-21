package verify

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

type fakeStore struct {
	mu            sync.Mutex
	verifications map[string]*Verification
	logs          map[string]string
	verifiedBy    map[string]string
	files         map[string]map[string]SourceFile
	contracts     map[string]*string

	failInterrupted int64
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		verifications: make(map[string]*Verification),
		logs:          make(map[string]string),
		verifiedBy:    make(map[string]string),
		files:         make(map[string]map[string]SourceFile),
		contracts:     make(map[string]*string),
	}
}

func (f *fakeStore) addContract(id string, wasmHash *string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.contracts[id] = wasmHash
}

func (f *fakeStore) CreateVerification(_ context.Context, v *Verification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *v
	f.verifications[v.ID] = &cp
	return nil
}

func (f *fakeStore) SetBuilding(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.verifications[id]; ok && v.Status == StatusQueued {
		v.Status = StatusBuilding
	}
	return nil
}

func (f *fakeStore) Complete(_ context.Context, c *Completion) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.verifications[c.ID]
	if !ok {
		return fmt.Errorf("verification %s not found", c.ID)
	}
	v.Status = c.Status
	v.Reason = c.Reason
	match := c.Match
	v.Match = &match
	now := v.CreatedAt.Add(time.Second)
	v.CompletedAt = &now
	if c.WasmHash != "" {
		h := c.WasmHash
		v.WasmHash = &h
	}
	if c.Toolchain != nil {
		tc := *c.Toolchain
		v.ToolchainActual = &tc
	}
	f.logs[c.ID] = c.Logs

	if c.Status == StatusVerified {
		if _, exists := f.verifiedBy[c.WasmHash]; !exists {
			f.verifiedBy[c.WasmHash] = c.ID
			f.files[c.WasmHash] = make(map[string]SourceFile)
			for _, file := range c.Files {
				f.files[c.WasmHash][file.Path] = file
			}
		}
	}
	return nil
}

func (f *fakeStore) GetVerification(_ context.Context, id string) (*Verification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.verifications[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *v
	return &cp, nil
}

func (f *fakeStore) GetLogs(_ context.Context, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.logs[id]
	if !ok {
		if _, exists := f.verifications[id]; !exists {
			return "", ErrNotFound
		}
		return "", nil
	}
	return l, nil
}

func (f *fakeStore) GetVerifiedSource(_ context.Context, wasmHash string) (*VerifiedSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.verifiedBy[wasmHash]
	if !ok {
		return nil, ErrNotFound
	}
	v := f.verifications[id]
	tc := v.ToolchainActual
	bs := v.Build
	src := v.Source
	var total int64
	for _, file := range f.files[wasmHash] {
		total += int64(len(file.Content))
	}
	return &VerifiedSource{
		WasmHash:       wasmHash,
		VerificationID: id,
		ContractID:     v.ContractID,
		Network:        v.Network,
		FileCount:      len(f.files[wasmHash]),
		TotalSize:      total,
		Toolchain:      tc,
		Build:          &bs,
		Source:         &src,
	}, nil
}

func (f *fakeStore) ListSourceFiles(_ context.Context, wasmHash string) ([]SourceFileMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.files[wasmHash]
	if !ok || len(m) == 0 {
		return nil, nil
	}
	out := make([]SourceFileMeta, 0, len(m))
	for p, file := range m {
		out = append(out, SourceFileMeta{Path: p, Size: int64(len(file.Content))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (f *fakeStore) GetSourceFile(_ context.Context, wasmHash, path string) (*SourceFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.files[wasmHash]
	if !ok {
		return nil, ErrNotFound
	}
	file, ok := m[path]
	if !ok {
		return nil, ErrNotFound
	}
	return &SourceFile{Path: path, Content: file.Content}, nil
}

func (f *fakeStore) ExpectedWasmHash(_ context.Context, contractID string) (*string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.contracts[contractID]
	if !ok {
		return nil, ErrUnknownContract
	}
	if h == nil {
		return nil, ErrSACUnsupported
	}
	return h, nil
}

func (f *fakeStore) FailInterrupted(_ context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, v := range f.verifications {
		if v.Status == StatusQueued || v.Status == StatusBuilding {
			v.Status = StatusFailed
			v.Reason = "interrupted by indexer restart; please resubmit"
			f.failInterrupted++
		}
	}
	return f.failInterrupted, nil
}

func (f *fakeStore) Close() error { return nil }

var _ Store = (*fakeStore)(nil)

type fakeBuilder struct {
	mu      sync.Mutex
	wasm    []byte
	err     error
	request []BuildRequest
}

func (f *fakeBuilder) Build(_ context.Context, req BuildRequest) (*BuildResult, error) {
	f.mu.Lock()
	f.request = append(f.request, req)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return &BuildResult{
		Wasm:      f.wasm,
		Toolchain: Toolchain{Rust: "1.85.0", StellarCLI: "22.0.8"},
		Logs:      "fake build log",
	}, nil
}

func (f *fakeBuilder) buildCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.request)
}

var errFakeBuild = errors.New("cargo exited with code 101")
