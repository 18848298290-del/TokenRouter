package httpapi

import (
	"errors"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// ReadLenientJSONRequestBodyWithPrealloc reads the body and escapes raw control bytes
// inside JSON strings before strict JSON validation.
func ReadLenientJSONRequestBodyWithPrealloc(req *http.Request, maxNormalizedBytes int64) ([]byte, error) {
	body, err := ReadRequestBodyWithPrealloc(req)
	if err != nil {
		return nil, err
	}
	return NormalizeLenientJSONRequestBody(body, maxNormalizedBytes)
}

// NormalizeLenientJSONRequestBody converts size-limit failures to http.MaxBytesError with the same limit.
func NormalizeLenientJSONRequestBody(body []byte, limit int64) ([]byte, error) {
	result, err := openai.NormalizeLenientJSONRequestBody(body, limit)
	if exceeded, ok := errors.AsType[*openai.BodyLimitError](err); ok {
		return nil, &http.MaxBytesError{Limit: exceeded.Limit}
	}
	return result, err
}

// ReadRequestBodyWithPrealloc delegates body reading and decompression to httpx.
func ReadRequestBodyWithPrealloc(req *http.Request) ([]byte, error) {
	return httpx.ReadRequestBodyWithPrealloc(req)
}
