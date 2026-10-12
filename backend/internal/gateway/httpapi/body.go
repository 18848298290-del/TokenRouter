package httpapi

import (
	"errors"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// Client JSON can contain raw control bytes that a strict parser rejects inside
// strings. Gateway adapters delegate that compatibility pass to the OpenAI
// normalizer and translate its expanded-size limit into the HTTP error type
// consumed by endpoint handlers.

// ReadLenientJSONRequestBodyWithPrealloc reads and decodes the body before
// escaping raw control bytes inside JSON strings. maxNormalizedBytes limits the
// normalized result; read and decoding failures stop processing before normalization.
// The caller still performs structural JSON validation on successful output.
func ReadLenientJSONRequestBodyWithPrealloc(req *http.Request, maxNormalizedBytes int64) ([]byte, error) {
	body, err := ReadRequestBodyWithPrealloc(req)
	if err != nil {
		return nil, err
	}
	return NormalizeLenientJSONRequestBody(body, maxNormalizedBytes)
}

// NormalizeLenientJSONRequestBody normalizes client JSON and translates size failures
// to http.MaxBytesError with the same limit. A nonpositive limit selects the
// protocol normalizer's 64 MiB default. Output may share body storage, and malformed
// JSON structure remains for the caller's validator to reject.
func NormalizeLenientJSONRequestBody(body []byte, limit int64) ([]byte, error) {
	result, err := openai.NormalizeLenientJSONRequestBody(body, limit)
	// HTTP handlers recognize MaxBytesError to choose their body-too-large response.
	// Keep the protocol normalizer's limit so expansion failures report the same configured bound.
	if exceeded, ok := errors.AsType[*openai.BodyLimitError](err); ok {
		return nil, &http.MaxBytesError{Limit: exceeded.Limit}
	}
	return result, err
}

// ReadRequestBodyWithPrealloc delegates body reading and decompression to httpx.
// A nil request or body returns nil without error. The reader consumes the body;
// successful decoding updates encoding and length metadata, while callers that
// need to reread input install a replacement body themselves.
// httpx can truncate decoded bytes at 64 MiB without a size error. Callers
// validate completeness, supply any limit on encoded or unencoded input, and
// close the original body after its use.
func ReadRequestBodyWithPrealloc(req *http.Request) ([]byte, error) {
	return httpx.ReadRequestBodyWithPrealloc(req)
}
