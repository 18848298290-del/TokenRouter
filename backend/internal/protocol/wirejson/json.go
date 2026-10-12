package wirejson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unsafe"

	"github.com/tidwall/gjson"
)

// Request repairs often decode an object and then serialize it after removing
// one field. Numbers decoded as float64 can round unrelated large identifiers,
// so object decoding retains json.Number values and verifies that the input
// contains one complete document.
//
// Outgoing JSON keeps literal HTML characters and omits the encoder's trailing
// newline, allowing repaired bodies to be forwarded as compact payloads.
// ParseView serves a different purpose: it lets gjson read the caller's byte
// storage through a string view. The caller's mutation discipline protects
// that view and every derived string until consumers finish reading them.

// DecodeUseNumber decodes one complete JSON document into dst, retaining
// interface-held numbers as json.Number. A second value or malformed trailing
// data returns an error after the first decode may already have populated dst.
//
// Typed destination fields still follow encoding/json's decoding rules.
// Callers discard partially populated results on error and choose their own
// handling for the malformed input; the decoder performs no rollback of dst.
func DecodeUseNumber(data []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	// A successful first decode can leave another value or malformed suffix.
	// Require EOF here so a repair cannot silently discard trailing request content.
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

// Marshal encodes v as JSON with literal HTML characters and no trailing
// newline. It returns encoding errors to the caller; valid json.Number literals
// retain their numeric spelling through the standard encoder.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	if len(out) > 0 && out[len(out)-1] == '\n' {
		// The encoder appends a record separator. Remove that final newline so
		// repaired request payloads can be forwarded as standalone compact JSON bodies.
		out = out[:len(out)-1]
	}
	return out, nil
}

// ParseView returns gjson's parse result over a string view of raw's backing
// bytes. Empty input yields an empty result. Callers leave raw unchanged while
// the result or its derived strings remain in use.
func ParseView(raw []byte) gjson.Result {
	if len(raw) == 0 {
		return gjson.Result{}
	}
	// Parsed strings can reference the same byte storage as the request body.
	// The caller's immutability obligation keeps those views stable during later content extraction.
	return gjson.Parse(*(*string)(unsafe.Pointer(&raw)))
}
