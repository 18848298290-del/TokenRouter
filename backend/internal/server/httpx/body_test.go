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

// samplePayload supplies the same JSON bytes to identity and compression
// fixtures so their returned contents can be compared across the supported encodings.
const samplePayload = `{"model":"gpt-5.5","input":"hi","stream":false}`

// zeroReader generates zero bytes on demand for compression tests.
type zeroReader struct{}

// TestReadRequestBodyWithPrealloc_PassesThroughIdentity
// reads a request with no encoding header.
// It must return the shared payload without a decoding error, protecting the
// raw-body path used when clients supply ordinary JSON bytes.
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

// TestReadRequestBodyWithPrealloc_DecodesZstd
// decodes a complete frame built from the shared payload.
// Returned bytes must match that payload, with Content-Encoding cleared and
// ContentLength replaced by the decoded byte count for the request.
func TestReadRequestBodyWithPrealloc_DecodesZstd(t *testing.T) {
	// Default NewWriter options introduce no constructor error in the pinned
	// encoder. Its error result is discarded; EncodeAll builds the in-memory frame.
	enc, _ := zstd.NewWriter(nil)
	compressed := enc.EncodeAll([]byte(samplePayload), nil)
	// EncodeAll uses the encoder pool rather than stream state. With no stream
	// writer installed, Close returns nil and its result is discarded by this fixture.
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

// TestReadRequestBodyWithPrealloc_DecodesGzip
// reads a completed gzip stream from a memory buffer.
// Decoding must succeed and recover the shared JSON payload, protecting the
// supported gzip Content-Encoding branch for request consumers.
func TestReadRequestBodyWithPrealloc_DecodesGzip(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write([]byte(samplePayload)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	// Finish the gzip footer before constructing the request, so the decoder
	// receives the complete stream that this payload round-trip is meant to exercise.
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

// TestReadRequestBodyWithPrealloc_DecodesDeflate
// supplies a completed zlib-wrapped stream.
// The deflate encoding branch must return the shared JSON payload successfully,
// establishing the actual container format accepted by this helper.
func TestReadRequestBodyWithPrealloc_DecodesDeflate(t *testing.T) {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write([]byte(samplePayload)); err != nil {
		t.Fatalf("zlib write: %v", err)
	}
	// Finalize the zlib wrapper and checksum before the buffer becomes request
	// input, keeping this fixture on the supported complete deflate-container path.
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

// TestReadRequestBodyWithPrealloc_RejectsUnsupportedEncoding
// declares br over the fixture payload.
// Reading must return an error that names that encoding, preserving the
// unsupported-format diagnostic exposed to handlers before they consume decoded JSON.
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

// TestReadRequestBodyWithPrealloc_RejectsCorruptZstd
// marks non-frame bytes as zstd data.
// The request read must fail, protecting propagation of a decoder error for
// that malformed compressed input rather than returning ordinary payload bytes.
func TestReadRequestBodyWithPrealloc_RejectsCorruptZstd(t *testing.T) {
	req := newRequestWithBody(t, []byte("not actually zstd"), "zstd")
	_, err := ReadRequestBodyWithPrealloc(req)
	if err == nil {
		t.Fatal("expected error for corrupt zstd body, got nil")
	}
}

// TestReadRequestBodyWithPrealloc_NilBody creates a POST request with a nil body.
// Reading must succeed with nil output, preserving the empty-body result used
// by consumers that distinguish missing input from supplied byte data.
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

// TestReadRequestBodyWithPrealloc_RespectsIdentityEncoding
// supplies the explicit identity header value.
// Reading must succeed with the shared payload bytes, establishing the header's
// pass-through behavior for an otherwise ordinary JSON request.
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

// TestCompressedRequestRetainsReadBoundary
// compresses one byte beyond the decoded output cap.
// Reading must return a successful cap-sized prefix and update ContentLength
// while clearing transmitted encoding and length headers for that returned prefix.
func TestCompressedRequestRetainsReadBoundary(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	// Generate exactly one byte beyond the decoded cap, so a successful shorter
	// result establishes truncation rather than completion of the compressed source.
	if _, err := io.CopyN(writer, zeroReader{}, maxDecompressedBodySize+1); err != nil {
		t.Fatal(err)
	}
	// Complete the compressed stream after generating the oversized decoded data.
	// The later prefix assertion must not be caused by an unfinished gzip footer.
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

// TestRequestBodyPreservesOuterLimit consumes a request wrapped in a three-byte limit.
// The returned error must remain discoverable as MaxBytesError with that limit,
// preserving its numeric value for the caller's oversized-request response.
func TestRequestBodyPreservesOuterLimit(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString("oversize"))
	request.Body = http.MaxBytesReader(httptest.NewRecorder(), request.Body, 3)
	_, err := ReadRequestBodyWithPrealloc(request)
	var limit *http.MaxBytesError
	if !errors.As(err, &limit) || limit.Limit != 3 {
		t.Fatalf("expected original MaxBytesError, got %v", err)
	}
}

// TestReadRequestBodyWithPrealloc supplies ordinary JSON with a known ContentLength.
// Reading must return the full payload without error, checking that the
// allocation-hint path still exposes all bytes supplied by its memory reader.
func TestReadRequestBodyWithPrealloc(t *testing.T) {
	payload := `{"model":"gpt-5","input":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(payload))
	req.ContentLength = int64(len(payload))

	body, err := ReadRequestBodyWithPrealloc(req)
	require.NoError(t, err)
	require.Equal(t, payload, string(body))
}

// TestReadRequestBodyWithPrealloc_MaxBytesError
// wraps eight bytes with a four-byte outer limit.
// The read must fail with a discoverable MaxBytesError, retaining the typed
// request-limit failure used by HTTP handlers to choose their rejection status.
func TestReadRequestBodyWithPrealloc_MaxBytesError(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(strings.Repeat("x", 8)))
	req.Body = http.MaxBytesReader(rec, req.Body, 4)

	_, err := ReadRequestBodyWithPrealloc(req)
	require.Error(t, err)
	var maxErr *http.MaxBytesError
	require.ErrorAs(t, err, &maxErr)
}

// TestBindJSONStrictIsLocalAndValidatesStructure
// combines a required name with dynamic credential keys.
// Strict binding accepts the dynamic map and rejects unknown struct fields,
// trailing input, and a missing required name; ordinary Gin binding still accepts
// the fixture's unknown field after those strict calls.
func TestBindJSONStrictIsLocalAndValidatesStructure(t *testing.T) {
	// input models a required scalar and an open map for strict-binding fixtures.
	type input struct {
		// Name is required by Gin validation after decoding. An absent value
		// therefore fails even when the supplied JSON fields are otherwise accepted.
		Name string `json:"name" binding:"required"`
		// Credentials accepts dynamic object keys through JSON decoding; this
		// fixture verifies that strict struct-field rejection leaves those keys usable.
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

// newRequestWithBody constructs a POST fixture with encoded bytes and their declared length.
// A nonempty encoding supplies Content-Encoding. The body reader shares the
// supplied byte storage, so callers leave it unchanged until the request has
// consumed those bytes; constructor failures fail the test.
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

// Read fills p with zeros and returns its complete length without EOF.
// Callers such as CopyN impose a finite byte count when using this inexhaustible
// source to create compression input beyond the decoded output limit.
func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
