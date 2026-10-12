package wirejson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unsafe"

	"github.com/tidwall/gjson"
)

func DecodeUseNumber(data []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	if len(out) > 0 && out[len(out)-1] == '\n' {
		out = out[:len(out)-1]
	}
	return out, nil
}

// ParseView parses JSON using a view of raw. Callers must not mutate raw while the result or its derived strings are in use.
func ParseView(raw []byte) gjson.Result {
	if len(raw) == 0 {
		return gjson.Result{}
	}
	// Sharing the backing bytes avoids copying large messages and contents.
	return gjson.Parse(*(*string)(unsafe.Pointer(&raw)))
}
