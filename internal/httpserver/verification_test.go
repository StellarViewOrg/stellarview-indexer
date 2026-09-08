package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/miguelnietoa/stellar-explorer/indexer/internal/store"
)

type fakeVerification struct {
	wasmHashByContract map[string]string
	codeExists         map[string]bool
	latestByWasmHash   map[string]*store.ContractVerification
	sourceFiles        map[string][]store.VerificationSourceFile
	nextID             int64
	created            []*store.ContractVerification
	createErr          error
}

func (f *fakeVerification) GetContractWasmHash(ctx context.Context, contractID string) (string, error) {
	return f.wasmHashByContract[contractID], nil
}

func (f *fakeVerification) ContractCodeExists(ctx context.Context, wasmHash string) (bool, error) {
	return f.codeExists[wasmHash], nil
}

func (f *fakeVerification) CreateVerification(ctx context.Context, v *store.ContractVerification, files []store.VerificationSourceFile) (int64, error) {
	if f.createErr != nil {
		return 0, f.createErr
	}
	f.nextID++
	v.ID = f.nextID
	v.Status = store.VerificationStatusPending
	f.created = append(f.created, v)
	if f.latestByWasmHash == nil {
		f.latestByWasmHash = map[string]*store.ContractVerification{}
	}
	f.latestByWasmHash[v.WasmHash] = v
	return v.ID, nil
}

func (f *fakeVerification) GetLatestVerificationByWasmHash(ctx context.Context, wasmHash string) (*store.ContractVerification, error) {
	return f.latestByWasmHash[wasmHash], nil
}

func (f *fakeVerification) GetVerificationByID(ctx context.Context, id int64) (*store.ContractVerification, error) {
	for _, v := range f.latestByWasmHash {
		if v.ID == id {
			return v, nil
		}
	}
	return nil, nil
}

func (f *fakeVerification) ListVerificationSourceFiles(ctx context.Context, wasmHash string) ([]store.VerificationSourceFile, error) {
	return f.sourceFiles[wasmHash], nil
}

func (f *fakeVerification) GetVerificationSourceFile(ctx context.Context, wasmHash, filePath string) (*store.VerificationSourceFile, error) {
	for _, sf := range f.sourceFiles[wasmHash] {
		if sf.FilePath == filePath {
			return &sf, nil
		}
	}
	return nil, nil
}

func newTestServerWithVerification(fv *fakeVerification) *Server {
	srv := New("127.0.0.1:0", Options{DB: fakePinger{}})
	srv.SetVerificationStore(fv)
	return srv
}

func TestVerifySubmit_UnknownContract(t *testing.T) {
	fv := &fakeVerification{}
	srv := newTestServerWithVerification(fv)

	body := `{"contractId":"CUNKNOWN","network":"testnet","files":{"src/lib.rs":"fn main(){}"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/verify", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rec.Code, rec.Body.String())
	}
}

func TestVerifySubmit_Success(t *testing.T) {
	fv := &fakeVerification{
		wasmHashByContract: map[string]string{"CCONTRACT": strings.Repeat("a", 64)},
		codeExists:         map[string]bool{strings.Repeat("a", 64): true},
	}
	srv := newTestServerWithVerification(fv)

	body := `{"contractId":"CCONTRACT","network":"testnet","repositoryUrl":"https://example.com/repo","files":{"src/lib.rs":"fn main(){}"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/verify", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body=%s", rec.Code, rec.Body.String())
	}
	var got verificationRecord
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != store.VerificationStatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
	if got.WasmHash != strings.Repeat("a", 64) {
		t.Errorf("wasmHash = %q", got.WasmHash)
	}
	if len(fv.created) != 1 || len(fv.created[0].ContractID) == 0 {
		t.Fatalf("expected one verification recorded, got %d", len(fv.created))
	}
}

func TestVerifySubmit_RejectsPathTraversal(t *testing.T) {
	fv := &fakeVerification{
		wasmHashByContract: map[string]string{"CCONTRACT": strings.Repeat("a", 64)},
		codeExists:         map[string]bool{strings.Repeat("a", 64): true},
	}
	srv := newTestServerWithVerification(fv)

	body := `{"contractId":"CCONTRACT","network":"testnet","files":{"../../etc/passwd":"x"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/verify", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
	if len(fv.created) != 0 {
		t.Fatalf("expected no verification recorded, got %d", len(fv.created))
	}
}

func TestVerifyByWasmHash_Unverified(t *testing.T) {
	fv := &fakeVerification{}
	srv := newTestServerWithVerification(fv)

	req := httptest.NewRequest(http.MethodGet, "/v1/verify/wasm/"+strings.Repeat("b", 64), nil)
	rec := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["verified"] != false {
		t.Errorf("verified = %v, want false", got["verified"])
	}
}

func TestVerifySourceTreeAndFile(t *testing.T) {
	wasmHash := strings.Repeat("c", 64)
	fv := &fakeVerification{
		sourceFiles: map[string][]store.VerificationSourceFile{
			wasmHash: {
				{WasmHash: wasmHash, FilePath: "src/lib.rs", Content: "fn main(){}", SizeBytes: 11},
			},
		},
	}
	srv := newTestServerWithVerification(fv)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/verify/wasm/"+wasmHash+"/source", nil)
	srv.srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tree status = %d, want 200", rec.Code)
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/v1/verify/wasm/"+wasmHash+"/source/src/lib.rs", nil)
	srv.srv.Handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("file status = %d, want 200, body=%s", rec2.Code, rec2.Body.String())
	}
	var got map[string]interface{}
	if err := json.Unmarshal(rec2.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["content"] != "fn main(){}" {
		t.Errorf("content = %v", got["content"])
	}
}

func TestSanitizeSourcePath(t *testing.T) {
	cases := []struct {
		in    string
		valid bool
	}{
		{"src/lib.rs", true},
		{"Cargo.toml", true},
		{"../escape.rs", false},
		{"/etc/passwd", false},
		{"a/../../b", false},
		{"", false},
		{".", false},
		{"C:/foo.rs", false},
		{"c:foo.rs", false},
		{`C:\foo.rs`, false},
	}
	for _, c := range cases {
		_, ok := sanitizeSourcePath(c.in)
		if ok != c.valid {
			t.Errorf("sanitizeSourcePath(%q) valid = %v, want %v", c.in, ok, c.valid)
		}
	}
}

func TestVerifySubmit_RejectsOversizedField(t *testing.T) {
	fv := &fakeVerification{
		wasmHashByContract: map[string]string{"CCONTRACT": strings.Repeat("a", 64)},
		codeExists:         map[string]bool{strings.Repeat("a", 64): true},
	}
	srv := newTestServerWithVerification(fv)

	body := fmt.Sprintf(`{"contractId":"CCONTRACT","network":%q,"files":{"src/lib.rs":"fn main(){}"}}`,
		strings.Repeat("x", maxNetworkLen+1))
	req := httptest.NewRequest(http.MethodPost, "/v1/verify", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
	if len(fv.created) != 0 {
		t.Fatalf("expected no verification recorded, got %d", len(fv.created))
	}
}

func TestNormalizeSubmittedFiles_TooManyFiles(t *testing.T) {
	files := make(map[string]string, maxVerificationFiles+1)
	for i := 0; i < maxVerificationFiles+1; i++ {
		files[fmt.Sprintf("file%d.rs", i)] = "x"
	}
	_, errMsg := normalizeSubmittedFiles(files)
	if errMsg == "" {
		t.Fatal("expected error for too many files")
	}
}

func TestNormalizeSubmittedFiles_FileTooLarge(t *testing.T) {
	files := map[string]string{"big.rs": strings.Repeat("a", maxVerificationFileBytes+1)}
	_, errMsg := normalizeSubmittedFiles(files)
	if errMsg == "" {
		t.Fatal("expected error for oversized file")
	}
}
