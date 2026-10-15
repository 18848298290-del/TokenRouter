package upstream

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
)

// image_upload.go represents multipart image uploads and converts their
// payloads to moderation or forwarding data URLs.
// Multipart image parts retain their field names, filenames, declared content
// types, and byte payloads for image forwarding and moderation. Moderation
// includes nonempty uploads labelled as images, while forwarding encodes any
// nonempty upload using its supplied or detected MIME type.

// ImageUpload holds one multipart upload and the byte storage used by image
// consumers.
type ImageUpload struct {
	FieldName   string // multipart form field name
	FileName    string // multipart upload filename value
	ContentType string // declared upload MIME type
	// Data is shared by shallow request conversion and consumed by moderation
	// or forwarding. Keep its bytes stable while those consumers encode them.
	Data []byte // shared upload payload bytes
	// Width and Height describe pixel dimensions when supplied by a consumer.
	// The native multipart parser currently assigns both zero because its dimension
	// helper does not inspect headers or decode the image.
	Width  int // pixels
	Height int // pixels
}

// ModerationDataURL returns a base64 data URL for nonempty upload bytes
// labelled as an image. Blank ContentType uses content sniffing; an empty
// payload or a non-image type returns empty for moderation omission. A
// supplied image type is trusted rather than verified by decoding.
func (u ImageUpload) ModerationDataURL() string {
	if len(u.Data) == 0 {
		return ""
	}
	contentType := strings.TrimSpace(u.ContentType)
	if contentType == "" {
		// Multipart uploads can omit Content-Type. Detect it from bytes so
		// moderation can decide whether this upload belongs in its image inputs.
		contentType = http.DetectContentType(u.Data)
	}
	if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
		return ""
	}
	return fmt.Sprintf("data:%s;base64,%s", contentType, base64.StdEncoding.EncodeToString(u.Data))
}

// ImageUploadToDataURL returns a base64 data URL using the supplied or
// detected MIME type. Empty data returns an error identifying the trimmed
// filename; nonempty data is encoded even when its MIME type is not an image.
func ImageUploadToDataURL(upload ImageUpload) (string, error) {
	if len(upload.Data) == 0 {
		return "", fmt.Errorf("upload %q is empty", strings.TrimSpace(upload.FileName))
	}
	contentType := strings.TrimSpace(upload.ContentType)
	if contentType == "" {
		// Forwarding accepts uploads whose part header omits a content type.
		// Detect a label from the retained bytes before constructing the data URL.
		contentType = http.DetectContentType(upload.Data)
	}
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(upload.Data), nil
}
