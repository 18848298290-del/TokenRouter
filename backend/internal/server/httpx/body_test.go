package httpx

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
)

const samplePayload = `{"model":"gpt-5.5","input":"hi","stream":false}`

// zeroReader generates zero bytes on demand for compression tests.
type zeroReader struct{}

func TestReadRequestBodyWithPrealloc_PassesThroughIdentity(t *testing.T) {
	req := newRequestWithBody(t, []byte(samplePayload), "")
	got, err := ReadRequestBodyWithPrealloc(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != samplePayload {
		t.Fatalf("body mismatch: got %q", got)
	}
}

func TestReadRequestBodyWithPrealloc_DecodesZstd(t *testing.T) {
	enc, _ := zstd.NewWriter(nil)
	compressed := enc.EncodeAll([]byte(samplePayload), nil)
	_ = enc.Close()

	req := newRequestWithBody(t, compressed, "zstd")
	got, err := ReadRequestBodyWithPrealloc(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != samplePayload {
		t.Fatalf("body mismatch: got %q", got)
	}
	if req.Header.Get("Content-Encoding") != "" {
		t.Fatalf("Content-Encoding should be cleared after decoding")
	}
	if req.ContentLength != int64(len(samplePayload)) {
		t.Fatalf("ContentLength not updated: %d", req.ContentLength)
	}
}

func TestReadRequestBodyWithPrealloc_DecodesGzip(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write([]byte(samplePayload)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	req := newRequestWithBody(t, buf.Bytes(), "gzip")
	got, err := ReadRequestBodyWithPrealloc(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != samplePayload {
		t.Fatalf("body mismatch: got %q", got)
	}
}

func TestReadRequestBodyWithPrealloc_DecodesDeflate(t *testing.T) {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write([]byte(samplePayload)); err != nil {
		t.Fatalf("zlib write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zlib close: %v", err)
	}

	req := newRequestWithBody(t, buf.Bytes(), "deflate")
	got, err := ReadRequestBodyWithPrealloc(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != samplePayload {
		t.Fatalf("body mismatch: got %q", got)
	}
}

func TestReadRequestBodyWithPrealloc_RejectsUnsupportedEncoding(t *testing.T) {
	req := newRequestWithBody(t, []byte(samplePayload), "br")
	_, err := ReadRequestBodyWithPrealloc(req)
	if err == nil {
		t.Fatal("expected error for unsupported encoding, got nil")
	}
	if !strings.Contains(err.Error(), "br") {
		t.Fatalf("error should mention encoding, got %v", err)
	}
}

func TestReadRequestBodyWithPrealloc_RejectsCorruptZstd(t *testing.T) {
	req := newRequestWithBody(t, []byte("not actually zstd"), "zstd")
	_, err := ReadRequestBodyWithPrealloc(req)
	if err == nil {
		t.Fatal("expected error for corrupt zstd body, got nil")
	}
}

func TestReadRequestBodyWithPrealloc_NilBody(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "/v1/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	got, err := ReadRequestBodyWithPrealloc(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil body, got %q", got)
	}
}

func TestReadRequestBodyWithPrealloc_RespectsIdentityEncoding(t *testing.T) {
	req := newRequestWithBody(t, []byte(samplePayload), "identity")
	got, err := ReadRequestBodyWithPrealloc(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != samplePayload {
		t.Fatalf("body mismatch: got %q", got)
	}
}

// TestCompressedRequestRetainsReadBoundary checks decoded truncation and header updates.
func TestCompressedRequestRetainsReadBoundary(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := io.CopyN(writer, zeroReader{}, maxDecompressedBodySize+1); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/", &compressed)
	request.Header.Set("Content-Encoding", "gzip")
	request.Header.Set("Content-Length", "123")
	body, err := ReadRequestBodyWithPrealloc(request)
	if err != nil || len(body) != maxDecompressedBodySize {
		t.Fatalf("boundary changed: length=%d err=%v", len(body), err)
	}
	if request.ContentLength != maxDecompressedBodySize || request.Header.Get("Content-Encoding") != "" || request.Header.Get("Content-Length") != "" {
		t.Fatal("decompressed request metadata changed")
	}
}

// TestRequestBodyPreservesOuterLimit checks that the outer request-size error reaches the caller.
func TestRequestBodyPreservesOuterLimit(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString("oversize"))
	request.Body = http.MaxBytesReader(httptest.NewRecorder(), request.Body, 3)
	_, err := ReadRequestBodyWithPrealloc(request)
	var limit *http.MaxBytesError
	if !errors.As(err, &limit) || limit.Limit != 3 {
		t.Fatalf("expected original MaxBytesError, got %v", err)
	}
}

func TestReadRequestBodyWithPrealloc(t *testing.T) {
	payload := `{"model":"gpt-5","input":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(payload))
	req.ContentLength = int64(len(payload))

	body, err := ReadRequestBodyWithPrealloc(req)
	require.NoError(t, err)
	require.Equal(t, payload, string(body))
}

func TestReadRequestBodyWithPrealloc_MaxBytesError(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(strings.Repeat("x", 8)))
	req.Body = http.MaxBytesReader(rec, req.Body, 4)

	_, err := ReadRequestBodyWithPrealloc(req)
	require.Error(t, err)
	var maxErr *http.MaxBytesError
	require.ErrorAs(t, err, &maxErr)
}

// TestBindJSONStrictIsLocalAndValidatesStructure checks unknown fields, maps, and struct validation.
// Ordinary Gin binding continues to accept unknown fields.
func TestBindJSONStrictIsLocalAndValidatesStructure(t *testing.T) {
	type input struct {
		Name        string         `json:"name" binding:"required"`
		Credentials map[string]any `json:"credentials"`
	}
	contextFor := func(body string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		return c
	}
	var valid input
	require.NoError(t, BindJSONStrict(contextFor(`{"name":"ok","credentials":{"custom":true}}`), &valid))
	require.Equal(t, true, valid.Credentials["custom"])
	for _, body := range []string{`{"name":"ok","retired":false}`, `{"name":"ok"} {}`, `{"credentials":{}}`, `{"name":"ok"} trailing`} {
		require.Error(t, BindJSONStrict(contextFor(body), &input{}))
	}
	require.NoError(t, contextFor(`{"name":"ok","unrelated_unknown":true}`).ShouldBindJSON(&input{}))
}

func newRequestWithBody(t *testing.T, body []byte, encoding string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if encoding != "" {
		req.Header.Set("Content-Encoding", encoding)
	}
	req.ContentLength = int64(len(body))
	return req
}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
