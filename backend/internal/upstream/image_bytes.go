package upstream

import (
	"encoding/base64"
	"io"
)

// DecodedImage contains decoded image bytes and their detected MIME type.
type DecodedImage struct {
	Bytes []byte
	Mime  string
}

// ImageOutput is one image returned by an executor.
type ImageOutput struct {
	Index int
	Bytes []byte
	Mime  string
}

// ReadLimitedBody reads at most limit bytes, defaulting to 64 MiB for nonpositive limits. It does
// not report truncation.
func ReadLimitedBody(body io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = 64 << 20
	}
	return io.ReadAll(io.LimitReader(body, limit))
}

// DecodeBase64Image decodes image data and detects its MIME type, defaulting unknown types to image/png.
func DecodeBase64Image(raw string) (DecodedImage, error) {
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		// Some upstreams omit base64 padding.
		data, err = base64.RawStdEncoding.DecodeString(raw)
		if err != nil {
			return DecodedImage{}, err
		}
	}
	mime := SniffImageMIME(data)
	if mime == "" {
		mime = "image/png"
	}
	return DecodedImage{Bytes: data, Mime: mime}, nil
}

// SniffImageMIME recognizes PNG, JPEG, and WebP signatures or returns an empty string.
func SniffImageMIME(data []byte) string {
	if len(data) >= 8 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' {
		return "image/png"
	}
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}
	if len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	return ""
}
