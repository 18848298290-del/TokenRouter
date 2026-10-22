package httpclient

import (
	"bufio"
	"compress/flate"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// response_body.go decodes upstream response bodies and reads buffered payloads
// with a configurable byte limit. Supported content encodings are installed
// before the pool wraps a response for reservation tracking. Decoder setup
// changes encoding and length metadata after it succeeds, while the body wrapper
// keeps the network response available for cleanup after decoding finishes.
//
// Zstd header inspection buffers input so rejected headers leave their peeked
// bytes readable. Gzip initialization can consume bytes before rejecting a
// header, leaving the underlying body's remaining bytes available. Buffered
// reads use one extra byte to distinguish an exactly fitting payload from
// overflow; failures return no body and leave reader cleanup with the caller.

// DefaultResponseReadMaxBytes bounds buffered upstream responses when callers
// supply a nonpositive size, before the complete body reaches protocol decoding.
const DefaultResponseReadMaxBytes int64 = 128 * 1024 * 1024

// ErrResponseBodyTooLarge identifies overflow so callers can distinguish the
// configured response size limit from failures reading the underlying body.
var ErrResponseBodyTooLarge = errors.New("upstream response body too large")

// zstdResponseReader reports decoder reads while warning once about non-EOF
// failures.
type zstdResponseReader struct {
	// ReadCloser supplies decoded bytes while warnOnce limits non-EOF failure
	// logging to this reader instance. Underlying decoder read and close
	// requirements remain applicable.
	io.ReadCloser           // retained zstd decoder reader
	warnOnce      sync.Once // decoder failure warning guard
}

// decompressedBody closes the decoder and the underlying response body.
type decompressedBody struct {
	// reader produces decoded or buffered bytes while closer retains the
	// original network body. Close attempts reader cleanup first, discards its
	// error, then returns the original body's close result.
	reader io.Reader // decoded or buffered response bytes
	closer io.Closer // retained original response body
}

// ReadResponseBodyLimited returns the complete body when it fits maxBytes,
// reading one extra byte to detect overflow. Positive limits must leave room
// for maxBytes+1 in int64. Nonpositive limits select
// DefaultResponseReadMaxBytes; nil readers, read errors, and overflow return
// an error with no body. The caller owns closing the reader.
func ReadResponseBodyLimited(reader io.Reader, maxBytes int64) ([]byte, error) {
	if reader == nil {
		return nil, errors.New("response body is nil")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultResponseReadMaxBytes
	}
	// One extra byte distinguishes overflow from an exactly fitting response,
	// allowing callers to reject oversized input before their complete-body decode.
	body, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("%w: limit=%d", ErrResponseBodyTooLarge, maxBytes)
	}
	return body, nil
}

// decompressResponseBody installs one decoder for supported Content-Encoding
// on a non-nil response body. Supported names are gzip, br, deflate, and
// zstd; unsupported or combined encodings retain the body. Setup failure
// retains encoding metadata, while gzip header reads may already have
// consumed original bytes.
//
// Zstd peeks through a buffered reader before decoder setup so a failed setup
// keeps peeked bytes readable. Installed decoders remove encoding/length
// headers and set unknown length. Closing the wrapper attempts decoder
// cleanup before closing the original body; decoder-close errors are
// discarded while original body errors remain caller-visible.
func decompressResponseBody(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	ce := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	if ce == "" {
		return
	}

	originalBody := resp.Body
	var reader io.Reader
	switch ce {
	case "gzip":
		gr, err := gzip.NewReader(resp.Body)
		if err != nil {
			// Gzip setup can already have consumed header bytes. Retain encoding
			// metadata and the remaining original body when initialization cannot supply a decoder.
			return
		}
		reader = gr
	case "br":
		reader = brotli.NewReader(resp.Body)
	case "deflate":
		reader = flate.NewReader(resp.Body)
	case "zstd":
		bufferedBody := bufio.NewReader(resp.Body)
		resp.Body = &decompressedBody{reader: bufferedBody, closer: originalBody}

		// Peek retains bytes in this reader. Setup failure can then leave the
		// encoded response readable through the buffered body instead of losing consumed input.
		headerBytes, _ := bufferedBody.Peek(zstd.HeaderMaxSize)
		var header zstd.Header
		if err := header.Decode(headerBytes); err != nil {
			slog.Warn("zstd_decompress_failed", "error", err)
			return
		}

		zr, err := zstd.NewReader(bufferedBody)
		if err != nil {
			slog.Warn("zstd_decompress_failed", "error", err)
			return
		}
		reader = &zstdResponseReader{ReadCloser: zr.IOReadCloser()}
	default:
		return
	}

	resp.Body = &decompressedBody{reader: reader, closer: originalBody}
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Content-Length") // Decoded length is unknown.
	resp.ContentLength = -1
}

// Read delegates to the zstd decoder and emits one warning for the first
// non-EOF error. It returns the decoder's count and error unchanged, while
// later failures reuse the completed warning guard.
func (r *zstdResponseReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		r.warnOnce.Do(func() {
			slog.Warn("zstd_decompress_failed", "error", err)
		})
	}
	return n, err
}

// Read delegates response bytes to the installed decoder or buffered reader.
func (d *decompressedBody) Read(p []byte) (int, error) {
	return d.reader.Read(p)
}

// Close attempts decoder cleanup when it implements io.Closer, then closes
// the retained original response body. Decoder-close errors are discarded;
// the returned error comes from the original body, and callers retain its
// repeat/concurrent-close rules.
func (d *decompressedBody) Close() error {
	// Decoder cleanup can own buffers or workers. Attempt it first, then close
	// the original network body and expose that body's cleanup result to its caller.
	if rc, ok := d.reader.(io.Closer); ok {
		_ = rc.Close()
	}
	return d.closer.Close()
}
