package verify

import (
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type apiTest struct {
	svc *Service
	st  *fakeStore
	srv *Server
}

func newAPITest(t *testing.T) *apiTest {
	t.Helper()
	st := newFakeStore()
	hash := testWasmHash()
	st.addContract(testContractID(t, 1), &hash)
	b := &fakeBuilder{wasm: []byte("wasm-bytes")}
	svc := newTestService(t, st, b)
	svc.Start(1)
	t.Cleanup(func() { svc.Stop(3 * time.Second) })
	return &apiTest{svc: svc, st: st, srv: NewServer("127.0.0.1:0", svc)}
}

func testWasmHash() string { return sha256HexOf("wasm-bytes") }

func (a *apiTest) do(t *testing.T, req *http.Request) (*httptest.ResponseRecorder, func() map[string]interface{}) {
	t.Helper()
	rec := httptest.NewRecorder()
	a.srv.srv.Handler.ServeHTTP(rec, req)
	decode := func() map[string]interface{} {
		var m map[string]interface{}
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return m
	}
	return rec, decode
}

func multipartSubmit(t *testing.T, contractID string) (*http.Request, error) {
	t.Helper()
	var buf strings.Builder
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("archive", "contract.tar.gz")
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(makeZipSource(t)); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/contracts/"+contractID+"/verifications", strings.NewReader(buf.String()))
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req, nil
}

func TestSubmitArchiveMultipartAccepted(t *testing.T) {
	a := newAPITest(t)

	req, err := multipartSubmit(t, testContractID(t, 1))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	rec, body := a.do(t, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	id, _ := body()["id"].(string)
	if id == "" {
		t.Fatal("expected id in response")
	}
	statusURL, _ := body()["status_url"].(string)
	if statusURL != "/api/v1/verifications/"+id {
		t.Fatalf("unexpected status_url %q", statusURL)
	}

	poll := waitForTerminal(t, a.st, id)
	if poll.Status != StatusVerified {
		t.Fatalf("expected verified end-to-end, got %s (%s)", poll.Status, poll.Reason)
	}
}

func TestSubmitJSONGitAccepted(t *testing.T) {
	a := newAPITest(t)
	payload := `{
		"source": {"type": "git", "repository": "https://github.com/example/repo", "commit": "` + strings.Repeat("b", 40) + `"},
		"network": "testnet",
		"build": {"profile": "release"}
	}`
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/contracts/"+testContractID(t, 1)+"/verifications", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec, _ := a.do(t, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSubmitWrongContentTypeRejected(t *testing.T) {
	a := newAPITest(t)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/contracts/"+testContractID(t, 1)+"/verifications", strings.NewReader("{}"))
	rec, body := a.do(t, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d", rec.Code)
	}
	if code, _ := body()["error"].(map[string]interface{})["code"].(string); code != "invalid_request" {
		t.Fatalf("expected invalid_request code, got %v", body())
	}
}

func TestSubmitInvalidContractIDShape(t *testing.T) {
	a := newAPITest(t)
	req, err := multipartSubmit(t, "GABCDEF")
	if err != nil {
		t.Fatal(err)
	}
	rec, body := a.do(t, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	msg, _ := body()["error"].(map[string]interface{})["message"].(string)
	if !strings.Contains(msg, "contract address") {
		t.Fatalf("expected helpful message, got %q", msg)
	}
}

func TestStatusUnknownIDNotFound(t *testing.T) {
	a := newAPITest(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/verifications/nope", nil)
	rec, body := a.do(t, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	code, _ := body()["error"].(map[string]interface{})["code"].(string)
	if code != "not_found" {
		t.Fatalf("expected not_found, got %q", code)
	}
}

func TestStatusResponseFrozenShape(t *testing.T) {
	a := newAPITest(t)
	req, _ := multipartSubmit(t, testContractID(t, 1))
	rec, _ := a.do(t, req)
	var submitted struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &submitted)
	v := waitForTerminal(t, a.st, submitted.ID)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/verifications/"+submitted.ID, nil)
	rec, _ = a.do(t, req)

	var resp VerificationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if resp.ID != v.ID || resp.ContractID != v.ContractID || resp.Network != "testnet" ||
		resp.Status != "verified" || resp.Match == nil || !*resp.Match ||
		resp.WasmHash == nil || resp.ExpectedWasmHash == nil ||
		resp.Source.Type != SourceTypeArchive || resp.Source.Name != "contract.tar.gz" ||
		resp.Source.SHA256 == "" ||
		resp.BuildLogsURL != "/api/v1/verifications/"+submitted.ID+"/logs" ||
		resp.CreatedAt.IsZero() {
		t.Fatalf("frozen shape violated: %+v", resp)
	}
}

func TestLookupUnverifiedReturns200False(t *testing.T) {
	a := newAPITest(t)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/wasm/"+strings.Repeat("00", 32)+"/verification", nil)
	rec, body := a.do(t, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if v, _ := body()["verified"].(bool); v {
		t.Fatal("expected verified=false")
	}
	if h, _ := body()["wasm_hash"].(string); h != strings.Repeat("00", 32) {
		t.Fatalf("expected wasm_hash echo, got %v", body())
	}
}

func TestLookupVerifiedFullPayload(t *testing.T) {
	a := newAPITest(t)
	req, _ := multipartSubmit(t, testContractID(t, 1))
	rec, _ := a.do(t, req)
	var submitted struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &submitted)
	waitForTerminal(t, a.st, submitted.ID)

	hash := testWasmHash()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/wasm/"+hash+"/verification", nil)
	rec, body := a.do(t, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	m := body()
	if v, _ := m["verified"].(bool); !v {
		t.Fatalf("expected verified=true, got %v", m)
	}
	for _, key := range []string{"verification_id", "contract_id", "network", "toolchain", "build", "source", "file_count", "total_size", "verified_at"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing frozen field %q in lookup response: %v", key, m)
		}
	}
}

func TestTreeAndFileEndpoints(t *testing.T) {
	a := newAPITest(t)
	req, _ := multipartSubmit(t, testContractID(t, 1))
	rec, _ := a.do(t, req)
	var submitted struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &submitted)
	waitForTerminal(t, a.st, submitted.ID)

	hash := testWasmHash()

	treeReq := httptest.NewRequest(http.MethodGet, "/api/v1/wasm/"+hash+"/source/tree", nil)
	rec, body := a.do(t, treeReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("tree expected 200, got %d", rec.Code)
	}
	files, _ := body()["files"].([]interface{})
	if len(files) != 2 {
		t.Fatalf("expected 2 files in tree, got %v", body())
	}

	fileReq := httptest.NewRequest(http.MethodGet,
		"/api/v1/wasm/"+hash+"/source/file?path=src/lib.rs", nil)
	rec = httptest.NewRecorder()
	a.srv.srv.Handler.ServeHTTP(rec, fileReq)
	data, _ := io.ReadAll(rec.Body)
	if rec.Code != http.StatusOK || !strings.Contains(string(data), "// c") {
		t.Fatalf("file fetch failed: %d %q", rec.Code, data)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("unexpected content type %q", ct)
	}
	if fp := rec.Header().Get("X-File-Path"); fp != "src/lib.rs" {
		t.Fatalf("unexpected X-File-Path %q", fp)
	}
}

func TestFileEndpointRejectsTraversal(t *testing.T) {
	a := newAPITest(t)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/wasm/"+testWasmHash()+"/source/file?path=../../etc/passwd", nil)
	rec, body := a.do(t, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	code, _ := body()["error"].(map[string]interface{})["code"].(string)
	if code != "invalid_request" {
		t.Fatalf("expected invalid_request, got %q", code)
	}
}

func TestTreeUnknownHashNotFound(t *testing.T) {
	a := newAPITest(t)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/wasm/"+strings.Repeat("ff", 32)+"/source/tree", nil)
	rec, _ := a.do(t, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestRateLimitedSubmission(t *testing.T) {
	st := newFakeStore()
	st.addContract(testContractID(t, 1), strPtr(strings.Repeat("88", 32)))
	svc, err := NewService(ServiceConfig{
		Store:           st,
		Builder:         &fakeBuilder{},
		Limiter:         NewIPRateLimiter(0.001, 1),
		Network:         "testnet",
		WorkspaceDir:    t.TempDir(),
		MaxArchiveBytes: 1 << 20,
		ExtractLimits:   DefaultExtractLimits(4 << 20),
		QueueSize:       8,
		BuildTimeout:    30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer("127.0.0.1:0", svc)

	first, _ := multipartSubmit(t, testContractID(t, 1))
	rec := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, first)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("first submit should pass, got %d", rec.Code)
	}

	second, _ := multipartSubmit(t, testContractID(t, 1))
	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, second)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" {
		t.Fatal("expected Retry-After header on 429")
	}
}
