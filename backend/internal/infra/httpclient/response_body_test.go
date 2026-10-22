package httpclient

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Buffered-body fixtures use short in-memory payloads to isolate the
// configured byte budget. An exactly fitting payload is returned intact; a
// larger payload returns no bytes and an error recognizable through
// ErrResponseBodyTooLarge before protocol decoding can consume it.
// Decoder fixtures compare complete payloads and retained encoded bodies,
// including encoding metadata after successful or rejected initialization.

// responseTestBody pairs a fixture reader with successful in-memory cleanup.
type responseTestBody struct {
	io.Reader // in-memory fixture payload reader
}

// TestReadUpstreamResponseBodyLimited compares an exactly fitting payload
// with an oversized payload. It requires complete bytes for the former and no
// body plus the overflow sentinel for the latter.
func TestReadUpstreamResponseBodyLimited(t *testing.T) {
	t.Run("within limit", func(t *testing.T) {
		body, err := ReadResponseBodyLimited(bytes.NewReader([]byte("ok")), 2)
		require.NoError(t, err)
		require.Equal(t, []byte("ok"), body)
	})

	t.Run("exceeds limit", func(t *testing.T) {
		body, err := ReadResponseBodyLimited(bytes.NewReader([]byte("toolong")), 3)
		require.Nil(t, body)
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrResponseBodyTooLarge))
	})
}

// TestDecompressResponseBodyZstdUsage decodes a zstd-compressed usage payload
// with known token fields. It requires complete decoded bytes, token values,
// removed encoding/length headers, unknown decoded length, and successful
// cleanup.
func TestDecompressResponseBodyZstdUsage(t *testing.T) {
	payload := []byte(`{"usage":{"input_tokens":123,"output_tokens":45,"cache_read_input_tokens":67}}`)
	compressed := compressZstd(t, payload)
	resp := newEncodedResponse("zstd", compressed)

	decompressResponseBody(resp)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, payload, body)
	require.Equal(t, int64(123), gjson.GetBytes(body, "usage.input_tokens").Int())
	require.Equal(t, int64(45), gjson.GetBytes(body, "usage.output_tokens").Int())
	require.Equal(t, int64(67), gjson.GetBytes(body, "usage.cache_read_input_tokens").Int())
	require.Empty(t, resp.Header.Get("Content-Encoding"))
	require.Empty(t, resp.Header.Get("Content-Length"))
	require.Equal(t, int64(-1), resp.ContentLength)
	require.NoError(t, resp.Body.Close())
}

// TestDecompressResponseBodyExistingEncodings decodes the same JSON through
// gzip, brotli, and raw deflate fixtures. It requires matching bytes, removed
// encoded metadata, unknown length, and successful body cleanup for each
// decoder.
func TestDecompressResponseBodyExistingEncodings(t *testing.T) {
	payload := []byte(`{"ok":true}`)
	tests := []struct {
		name     string                          // case display name
		encoding string                          // selected response encoding text
		compress func(*testing.T, []byte) []byte // payload encoding fixture function
	}{
		{name: "gzip", encoding: "gzip", compress: compressGzip},
		{name: "brotli", encoding: "br", compress: compressBrotli},
		{name: "deflate", encoding: "deflate", compress: compressDeflate},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := newEncodedResponse(tt.encoding, tt.compress(t, payload))

			decompressResponseBody(resp)

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, payload, body)
			require.Empty(t, resp.Header.Get("Content-Encoding"))
			require.Empty(t, resp.Header.Get("Content-Length"))
			require.Equal(t, int64(-1), resp.ContentLength)
			require.NoError(t, resp.Body.Close())
		})
	}
}

// TestDecompressResponseBodyWithoutEncodingLeavesBodyUntouched supplies a
// plain body with no encoding header. It requires the same body pointer,
// length, and bytes, protecting the no-decoder response path.
func TestDecompressResponseBodyWithoutEncodingLeavesBodyUntouched(t *testing.T) {
	originalBody := &responseTestBody{Reader: bytes.NewReader([]byte("plain"))}
	resp := &http.Response{
		Header:        make(http.Header),
		Body:          originalBody,
		ContentLength: 5,
	}

	decompressResponseBody(resp)

	require.Same(t, originalBody, resp.Body)
	require.Equal(t, int64(5), resp.ContentLength)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "plain", string(body))
	require.NoError(t, resp.Body.Close())
}

// TestDecompressResponseBodyInvalidZstdWarnsAndPreservesBody captures slog
// while decoding invalid zstd input. It requires a warning, readable encoded
// bytes, retained encoding and length, and normal cleanup rather than a setup
// panic.
func TestDecompressResponseBodyInvalidZstdWarnsAndPreservesBody(t *testing.T) {
	previousLogger := slog.Default()
	var logOutput bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logOutput, nil)))
	t.Cleanup(func() {
		slog.SetDefault(previousLogger)
	})

	payload := []byte("not a zstd response")
	resp := newEncodedResponse("zstd", payload)

	require.NotPanics(t, func() {
		decompressResponseBody(resp)
	})

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, payload, body)
	require.Equal(t, "zstd", resp.Header.Get("Content-Encoding"))
	require.Equal(t, int64(len(payload)), resp.ContentLength)
	require.Contains(t, logOutput.String(), "msg=zstd_decompress_failed")
	require.NoError(t, resp.Body.Close())
}

// TestDecompressResponseBodyEmptyZstdWarnsAndPreservesBody supplies empty
// zstd-tagged input with captured slog output. It requires a warning, empty
// readable bytes, retained encoding/zero length, and normal cleanup after
// setup rejection.
func TestDecompressResponseBodyEmptyZstdWarnsAndPreservesBody(t *testing.T) {
	previousLogger := slog.Default()
	var logOutput bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logOutput, nil)))
	t.Cleanup(func() {
		slog.SetDefault(previousLogger)
	})

	resp := newEncodedResponse("zstd", nil)

	require.NotPanics(t, func() {
		decompressResponseBody(resp)
	})

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Empty(t, body)
	require.Equal(t, "zstd", resp.Header.Get("Content-Encoding"))
	require.Equal(t, int64(0), resp.ContentLength)
	require.Contains(t, logOutput.String(), "msg=zstd_decompress_failed")
	require.NoError(t, resp.Body.Close())
}

// Close reports successful cleanup for the in-memory response fixture body.
func (b *responseTestBody) Close() error {
	return nil
}

// newEncodedResponse builds a response with supplied encoded bytes and
// encoding metadata. Its body uses an in-memory reader, the Header length is
// a sentinel, and ContentLength tracks actual supplied payload bytes for
// fixture comparisons.
func newEncodedResponse(encoding string, body []byte) *http.Response {
	header := make(http.Header)
	header.Set("Content-Encoding", encoding)
	header.Set("Content-Length", "123")
	return &http.Response{
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

// compressZstd returns an encoded fixture payload and fails t on writer
// creation, write, or close errors. Closing finishes the buffered zstd frame
// before its bytes are returned for response decoding.
func compressZstd(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	require.NoError(t, err)
	_, err = zw.Write(payload)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// compressGzip returns encoded fixture bytes after writing payload and
// closing the gzip writer. Write and close failures stop t; closing finalizes
// frame data before the decoder fixture uses the buffer.
func compressGzip(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write(payload)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// compressBrotli returns encoded fixture bytes after writing payload and
// closing the brotli writer. Write and close failures stop t; writer cleanup
// completes output before the response fixture reads the retained buffer.
func compressBrotli(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := brotli.NewWriter(&buf)
	_, err := zw.Write(payload)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// compressDeflate returns a raw deflate fixture after creating, writing, and
// closing its compressor. Any stage failure stops t, and successful close
// completes the bytes needed by the response decoder.
func compressDeflate(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := flate.NewWriter(&buf, flate.DefaultCompression)
	require.NoError(t, err)
	_, err = zw.Write(payload)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}
