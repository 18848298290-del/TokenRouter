package openai

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/TokenFlux/TokenRouter/internal/egress/urlpolicy"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// image_backfill.go fills missing Base64 image results from validated URLs or
// inline data while retaining response metadata.
// Image responses can contain downloadable URLs when a client expects Base64
// data. Optional nonstreaming backfill visits missing b64_json values, retains
// the URL and surrounding response metadata, and leaves items unchanged when a
// download or JSON splice fails.
//
// Download requests use the caller transport without adding provider
// credentials, carry a request-scoped public-host restriction, and retain
// signed URL escaping after target validation. The transport is responsible for
// DNS and redirect enforcement. Inline data URLs and downloaded bodies share
// size and byte-sniffing checks, so a declared media type alone cannot qualify
// image content. Each failed item reports its index instead of a raw signed-URL
// error, allowing usable images in the same response to remain deliverable.

// openAIImageURLDownloadTimeout bounds one URL fetch so optional backfill cannot
// retain the image response indefinitely while waiting for downloadable content.
const openAIImageURLDownloadTimeout = 60 * time.Second

// openAIImageBackfillContentTypes permits sniffed raster image formats accepted for
// Base64 backfill and rejects other content regardless of response media-type claims.
var openAIImageBackfillContentTypes = map[string]struct{}{
	"image/png": {}, "image/jpeg": {}, "image/webp": {}, "image/gif": {},
}

// ImageBackfillOptions controls optional URL-to-Base64 image response rewriting
// and download callbacks.
type ImageBackfillOptions struct {
	// Enabled, Stream, and ResponseFormat jointly gate Backfill. Rewriting runs
	// only when enabled for a nonstreaming response whose requested format is
	// not URL; direct FetchBase64 calls still use download validation.
	Enabled        bool                         // optional Base64 backfill selection
	Stream         bool                         // streaming response mode flag
	ResponseFormat string                       // requested image response format
	ValidateURL    func(string) (string, error) // required HTTP target validator
	// Do must enforce the request public-host marker for DNS and every redirect;
	// FetchBase64 adds no provider authentication to its download request.
	Do func(*http.Request) (*http.Response, error)
	// Failure optionally observes the failed image index; download errors are
	// excluded because their text can contain signed URL credentials.
	Failure func(int)
}

// ReportFailure notifies the optional observer with the failed image index.
// Missing observers are ignored, and callers retain the index for diagnostics
// without passing download errors that can contain signed URLs.
func (options ImageBackfillOptions) ReportFailure(index int) {
	if options.Failure != nil {
		options.Failure(index)
	}
}

// Backfill fills missing Base64 data in eligible nonstreaming image response
// items. Disabled options, malformed JSON, stream mode, explicit URL format, or
// a nonarray data field returns the supplied bytes. ValidateURL and Do are
// required for HTTP downloads.
//
// Existing nonblank Base64 items remain intact and each missing item is
// downloaded separately. A failed download or JSON splice reports only its
// index and leaves that item usable in its URL form. Successful splices retain
// other response fields, letting billing consumers read the pre-backfill
// response metadata while downstream clients receive available Base64 content.
// @project-doc docs/interfaces/openai_upstream.md#images_url_backfill
func (options ImageBackfillOptions) Backfill(ctx context.Context, body []byte) []byte {
	if !options.Enabled || !gjson.ValidBytes(body) || (options.Stream || strings.EqualFold(strings.TrimSpace(options.ResponseFormat), "url")) {
		return body
	}
	items := gjson.GetBytes(body, "data")
	if !items.IsArray() {
		return body
	}
	for index, item := range items.Array() {
		if !item.IsObject() || strings.TrimSpace(item.Get("b64_json").String()) != "" {
			continue
		}
		rawURL := strings.TrimSpace(item.Get("url").String())
		if rawURL == "" {
			continue
		}
		encoded, err := options.FetchBase64(ctx, rawURL)
		if err != nil {
			// Download errors can expose signed URLs. Report the index while retaining
			// the original URL item, so diagnostics avoid credentials and other images can still complete.
			options.ReportFailure(index)
			continue
		}
		updated, err := sjson.SetBytes(body, fmt.Sprintf("data.%d.b64_json", index), encoded)
		if err != nil {
			// A failed splice leaves this item in URL form; report its index and keep
			// processing other images so one optional rewrite cannot discard the response.
			options.ReportFailure(index)
			continue
		}
		body = updated
	}
	return body
}

// FetchBase64 returns standard Base64 for validated inline or downloaded image
// bytes. Data URLs bypass HTTP and must fit the image byte budget and supported
// sniffed formats. HTTP paths require ValidateURL and Do; validation checks
// policy but the original trimmed URL is sent to retain signature-sensitive
// escaping.
//
// The request has a sixty-second timeout and the public-host-only marker, with
// no authorization or cookie added here. The transport enforces DNS and
// redirects under that marker. Reading one extra byte distinguishes oversized
// bodies from valid images before MIME sniffing and encoding. This function
// closes every acquired response and rejects unsuccessful statuses, oversized
// content, and unsupported formats.
func (options ImageBackfillOptions) FetchBase64(ctx context.Context, rawURL string) (string, error) {
	if strings.HasPrefix(strings.ToLower(rawURL), "data:") {
		if encoded := NormalizeOpenAIImageBase64(rawURL); encoded != "" {
			if len(encoded) > base64.StdEncoding.EncodedLen(int(OpenAIImageMaxDownloadBytes)) {
				return "", errors.New("data url image exceeds size limit")
			}
			data, err := base64.StdEncoding.DecodeString(encoded)
			if err == nil && int64(len(data)) <= OpenAIImageMaxDownloadBytes && IsBackfillImageContent(data) {
				return encoded, nil
			}
		}
		return "", errors.New("data url payload is not valid base64")
	}
	if options.Do == nil {
		return "", errors.New("http upstream is not configured")
	}
	// Validation can normalize base URLs in ways that change signed downloads.
	// Check policy but send the original trimmed spelling, including escaping and trailing slash.
	downloadURL := strings.TrimSpace(rawURL)
	if _, err := options.ValidateURL(downloadURL); err != nil {
		return "", fmt.Errorf("invalid image url: %w", err)
	}
	if err := RejectPrivateImageHost(downloadURL); err != nil {
		return "", err
	}
	downloadCtx, cancel := context.WithTimeout(upstream.WithHTTPUpstreamPublicHostsOnly(ctx), openAIImageURLDownloadTimeout)
	// Release the per-image timeout after validation and reading complete; its
	// public-host marker stays scoped to this download rather than ordinary provider requests.
	defer cancel()
	req, err := http.NewRequestWithContext(downloadCtx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", fmt.Errorf("build image download request: %w", err)
	}
	req.Header.Set("Accept", "image/*,*/*;q=0.8")
	resp, err := options.Do(req)
	if err != nil {
		return "", fmt.Errorf("download image: %w", err)
	}
	// Release download transport ownership on status, size, and format failures
	// so optional response backfill cannot retain an acquired connection after returning.
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("download image: unexpected status %d", resp.StatusCode)
	}
	// Read one extra byte to detect an oversized image before Base64 encoding,
	// preventing a truncated prefix from being accepted as a complete backfill result.
	data, err := io.ReadAll(io.LimitReader(resp.Body, OpenAIImageMaxDownloadBytes+1))
	if err != nil {
		return "", fmt.Errorf("read image body: %w", err)
	}
	if int64(len(data)) > OpenAIImageMaxDownloadBytes {
		return "", fmt.Errorf("downloaded image exceeds %d bytes", OpenAIImageMaxDownloadBytes)
	}
	if !IsBackfillImageContent(data) {
		return "", errors.New("download image: content is not an allowed image format")
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// RejectPrivateImageHost rejects blocked literal or named hosts recognized by
// urlpolicy. It parses the URL but performs no DNS lookup; the marked HTTP
// transport must enforce resolved addresses and redirects separately.
func RejectPrivateImageHost(downloadURL string) error {
	parsed, err := url.Parse(downloadURL)
	if err != nil {
		return fmt.Errorf("invalid image url: %w", err)
	}
	if host := parsed.Hostname(); urlpolicy.IsBlockedHost(host) {
		return fmt.Errorf("image url host is not allowed: %s", host)
	}
	return nil
}

// IsBackfillImageContent reports whether byte sniffing identifies PNG, JPEG,
// WebP, or GIF.
func IsBackfillImageContent(data []byte) bool {
	_, ok := openAIImageBackfillContentTypes[strings.ToLower(http.DetectContentType(data))]
	return ok
}
