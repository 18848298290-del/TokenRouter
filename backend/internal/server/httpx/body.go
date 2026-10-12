package httpx

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/klauspost/compress/zstd"
)

const (
	requestBodyReadInitCap    = 512
	requestBodyReadMaxInitCap = 1 << 20
	maxDecompressedBodySize   = 64 << 20
)

// ReadRequestBodyWithPrealloc reads the body and decodes supported Content-Encoding values.
// Decoded data is truncated at 64 MiB; callers must validate completeness. Successful
// decoding removes encoding and length headers and updates ContentLength. The caller
// supplies any limit on the compressed or unencoded body and closes the original body.
func ReadRequestBodyWithPrealloc(req *http.Request) ([]byte, error) {
	if req == nil || req.Body == nil {
		return nil, nil
	}

	capHint := requestBodyReadInitCap
	if req.ContentLength > 0 {
		switch {
		case req.ContentLength < int64(requestBodyReadInitCap):
			capHint = requestBodyReadInitCap
		case req.ContentLength > int64(requestBodyReadMaxInitCap):
			capHint = requestBodyReadMaxInitCap
		default:
			capHint = int(req.ContentLength)
		}
	}

	buf := bytes.NewBuffer(make([]byte, 0, capHint))
	if _, err := io.Copy(buf, req.Body); err != nil {
		return nil, err
	}
	raw := buf.Bytes()

	enc := strings.ToLower(strings.TrimSpace(req.Header.Get("Content-Encoding")))
	if enc == "" || enc == "identity" {
		return raw, nil
	}

	decoded, err := decompressRequestBody(enc, raw)
	if err != nil {
		return nil, fmt.Errorf("decode Content-Encoding %q: %w", enc, err)
	}

	req.Header.Del("Content-Encoding")
	req.Header.Del("Content-Length")
	req.ContentLength = int64(len(decoded))

	return decoded, nil
}

func decompressRequestBody(encoding string, raw []byte) ([]byte, error) {
	switch encoding {
	case "zstd":
		dec, err := zstd.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer dec.Close()
		return io.ReadAll(io.LimitReader(dec, maxDecompressedBodySize))
	case "gzip", "x-gzip":
		gr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer func() {
			_ = gr.Close()
		}()
		return io.ReadAll(io.LimitReader(gr, maxDecompressedBodySize))
	case "deflate":
		zr, err := zlib.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer func() {
			_ = zr.Close()
		}()
		return io.ReadAll(io.LimitReader(zr, maxDecompressedBodySize))
	default:
		return nil, errors.New("unsupported Content-Encoding")
	}
}

// BindJSONStrict decodes one JSON value, rejects unknown struct fields, and applies binding validation.
// Callers validate map contents such as credentials and extra.
func BindJSONStrict(c *gin.Context, target any) error {
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("request must contain one JSON object")
	}
	return binding.Validator.ValidateStruct(target)
}
