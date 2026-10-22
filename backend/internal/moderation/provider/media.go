package provider

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/moderation/contract"
)

// media.go retrieves moderation image snapshots under per-image and batch
// limits.
// Retained review images can come from inline data or untrusted HTTP locations,
// and failed retrievals still need their reference and input position. Remote
// downloads validate the host before requesting, validate redirect targets, and
// resolve checked addresses again at direct dial time. Image-like MIME and byte
// prefixes are accepted within the size cap; those checks do not decode
// complete image structures.
//
// A batch copies entries and gives up to four workers one shared deadline. Each
// worker owns one entry, and atomic byte reservations decide which completed
// images fit the record budget. Cancellation stops dispatch and joins accepted
// work before exposing the result. The initial entry copy still shares caller
// pointers and existing content slices until individual results replace them.

// Snapshot limits bound individual image reads, completed content retained per
// record, worker concurrency, and HTTP or batch waiting budgets.
const (
	maxContentModerationSnapshotBytes      = 20 * 1024 * 1024
	maxContentModerationSnapshotTotalBytes = 256 * 1024 * 1024
	contentModerationSnapshotTimeout       = 15 * time.Second
	contentModerationSnapshotTotalTimeout  = 15 * time.Second
	contentModerationSnapshotConcurrency   = 4
)

// SnapshotMedia fetches images into copied slice entries and returns those
// entries in input order. Supply a nonnil context and keep input pointers and
// byte slices stable until it returns: the initial copy retains those
// references, while completed fetches replace Content. Errors mark an entry and
// clear its content, leaving other supplied metadata as it was.
//
// Up to four workers share one batch deadline and reserve completed bytes
// against a 256 MiB total with CAS. Reservation order determines which images
// fit that budget. Cancellation marks undispatched entries and reaches HTTP
// fetches; inline decoding already underway completes locally, and the function
// joins all workers before returning.
func SnapshotMedia(ctx context.Context, media []contract.ContentModerationMedia) []contract.ContentModerationMedia {
	out := append([]contract.ContentModerationMedia(nil), media...)
	if len(out) == 0 {
		return out
	}
	// Give all workers the same batch context so later images use the remaining
	// time budget while each individual HTTP fetch also applies its own limit.
	snapshotCtx, cancel := context.WithTimeout(ctx, contentModerationSnapshotTotalTimeout)
	// Cancel releases timeout resources after this operation has finished; child
	// fetches inherit whichever caller or operation deadline expires first.
	defer cancel()
	jobs := make(chan int)
	var wg sync.WaitGroup
	var retainedBytes atomic.Int64
	workerCount := min(contentModerationSnapshotConcurrency, len(out))
	// Assign each index to one worker; writes stay within that entry, while the
	// shared CAS budget arbitrates which concurrently completed image bytes are retained.
	for range workerCount {
		wg.Go(func() {
			for index := range jobs {
				data, mimeType, err := fetchContentModerationImage(snapshotCtx, out[index].OriginalRef)
				// Failed fetches retain the reference and position for review, but clear
				// binary content so an error entry cannot be served as a ready snapshot.
				if err != nil {
					out[index].SnapshotStatus = "error"
					out[index].SnapshotError = err.Error()
					out[index].Content = nil
					continue
				}
				// Reserve after fetching because the decoded size is now known. A failed
				// reservation keeps these bytes out of the returned retained-content entries.
				if !reserveContentModerationSnapshotBytes(&retainedBytes, int64(len(data))) {
					out[index].SnapshotStatus = "error"
					out[index].SnapshotError = "image snapshots for this record exceed the 256 MB limit"
					out[index].Content = nil
					continue
				}
				digest := sha256.Sum256(data)
				out[index].MIMEType = mimeType
				out[index].SHA256 = hex.EncodeToString(digest[:])
				out[index].ByteSize = int64(len(data))
				out[index].SnapshotStatus = "ready"
				out[index].SnapshotError = ""
				out[index].Content = data
			}
		})
	}
	// Stop undispatched indexes on cancellation; already handed-off indexes finish
	// through their workers before the result slice can be returned to its caller.
	enqueueStopped := false
	for index := range out {
		if err := snapshotCtx.Err(); err != nil {
			for pending := index; pending < len(out); pending++ {
				out[pending].SnapshotStatus = "error"
				out[pending].SnapshotError = err.Error()
				out[pending].Content = nil
			}
			break
		}
		select {
		case jobs <- index:
		case <-snapshotCtx.Done():
			for pending := index; pending < len(out); pending++ {
				out[pending].SnapshotStatus = "error"
				out[pending].SnapshotError = snapshotCtx.Err().Error()
				out[pending].Content = nil
			}
			enqueueStopped = true
		}
		if enqueueStopped {
			break
		}
	}
	// Closing jobs lets workers finish their assigned fetches, and joining them
	// prevents callers from reading entries while a snapshot worker still mutates them.
	close(jobs)
	wg.Wait()
	return out
}

// reserveContentModerationSnapshotBytes claims size bytes from the shared
// retained-content budget and reports whether the claim fits. Nil counters,
// negative sizes, or a claim larger than the total are rejected; zero is
// accepted for a usable counter.
//
// Concurrent fetches can observe the same remaining budget, so a failed CAS
// retries against the new total before making a decision. A successful claim
// stays counted for the batch, and the caller retains its image bytes only
// after that claim succeeds.
func reserveContentModerationSnapshotBytes(retained *atomic.Int64, size int64) bool {
	if retained == nil || size < 0 || size > maxContentModerationSnapshotTotalBytes {
		return false
	}
	for {
		current := retained.Load()
		if current > maxContentModerationSnapshotTotalBytes-size {
			return false
		}
		// Another image may consume budget after this read; retry a lost CAS so
		// both workers cannot retain bytes against the same observed remaining capacity.
		if retained.CompareAndSwap(current, current+size) {
			return true
		}
	}
}

// fetchContentModerationImage returns bytes and a normalized image MIME label
// for a trimmed reference. It checks cancellation before local data decoding or
// URL validation, and accepts HTTP(S) or data references. Remote fetches
// enforce individual byte and time limits, check image-like headers and bytes,
// and close response and idle transport resources before returning.
func fetchContentModerationImage(ctx context.Context, reference string) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	reference = strings.TrimSpace(reference)
	if strings.HasPrefix(reference, "data:") {
		return decodeContentModerationDataImage(reference)
	}
	parsed, err := url.Parse(reference)
	if err != nil {
		return nil, "", fmt.Errorf("invalid image reference: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, "", errors.New("only HTTP(S) or data image references are supported")
	}
	if err := validateContentModerationRemoteURL(ctx, parsed); err != nil {
		return nil, "", err
	}
	requestCtx, cancel := context.WithTimeout(ctx, contentModerationSnapshotTimeout)
	// Cancel releases the fetch deadline timer after validation or reading returns,
	// keeping timeout resources bounded across repeated snapshot image downloads.
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, "", err
	}
	client := newContentModerationSnapshotClient()
	// This fetch owns its transport; discard reusable idle connections after the
	// response finishes so retained review images do not retain a client per download.
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("failed to download image: %w", err)
	}
	// Close the received body after header checks and bounded reading. Its close
	// error is suppressed because the fetch reports the earlier validation or read result.
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("image download returned HTTP %d", resp.StatusCode)
	}
	// Reject a declared oversized response before reading, then enforce the same
	// cap while reading because missing or incorrect length headers cannot bound allocation.
	if resp.ContentLength > maxContentModerationSnapshotBytes {
		return nil, "", errors.New("image exceeds the 20 MB limit")
	}
	mimeType, err := normalizeContentModerationImageMIME(resp.Header.Get("Content-Type"))
	if err != nil {
		return nil, "", err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxContentModerationSnapshotBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("failed to read image: %w", err)
	}
	if len(data) > maxContentModerationSnapshotBytes {
		return nil, "", errors.New("image exceeds the 20 MB limit")
	}
	if err := validateContentModerationImageBytes(data, mimeType); err != nil {
		return nil, "", err
	}
	return data, mimeType, nil
}

// decodeContentModerationDataImage decodes a data reference already identified
// by the caller. A final case-insensitive base64 marker selects standard base64
// decoding; other payloads use path unescaping. Decoded content is capped at
// twenty MiB and checked for image-like bytes, while malformed payloads return
// an error.
func decodeContentModerationDataImage(reference string) ([]byte, string, error) {
	header, payload, ok := strings.Cut(strings.TrimPrefix(reference, "data:"), ",")
	if !ok {
		return nil, "", errors.New("data image has no content")
	}
	parts := strings.Split(header, ";")
	mimeType, err := normalizeContentModerationImageMIME(parts[0])
	if err != nil {
		return nil, "", err
	}
	var data []byte
	if len(parts) > 1 && strings.EqualFold(parts[len(parts)-1], "base64") {
		// Decode through a bounded reader so an untrusted base64 payload cannot
		// allocate its complete decoded size before the twenty-MiB check is applied.
		decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(payload))
		data, err = io.ReadAll(io.LimitReader(decoder, maxContentModerationSnapshotBytes+1))
	} else {
		// Estimate unescaped bytes before allocating the decoded string; the later
		// PathUnescape call remains responsible for rejecting malformed percent sequences.
		if escapedContentModerationDataLength(payload) > maxContentModerationSnapshotBytes {
			return nil, "", errors.New("image exceeds the 20 MB limit")
		}
		var decoded string
		decoded, err = url.PathUnescape(payload)
		data = []byte(decoded)
	}
	if err != nil {
		return nil, "", fmt.Errorf("failed to decode data image: %w", err)
	}
	if len(data) > maxContentModerationSnapshotBytes {
		return nil, "", errors.New("image exceeds the 20 MB limit")
	}
	if err := validateContentModerationImageBytes(data, mimeType); err != nil {
		return nil, "", err
	}
	return data, mimeType, nil
}

// escapedContentModerationDataLength estimates bytes after path unescaping to
// bound allocation before decoding. Each percent sign with two following bytes
// counts as one even when those bytes are invalid escapes; PathUnescape
// subsequently rejects invalid sequences.
func escapedContentModerationDataLength(payload string) int {
	length := 0
	for index := 0; index < len(payload); {
		length++
		if payload[index] == '%' && index+2 < len(payload) {
			index += 3
		} else {
			index++
		}
	}
	return length
}

// normalizeContentModerationImageMIME parses and lowercases a supplied media
// type, discarding parameters. Any image/ subtype is accepted; malformed values
// and other media families return errors before the image body is retained.
func normalizeContentModerationImageMIME(value string) (string, error) {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("invalid image MIME type: %w", err)
	}
	mediaType = strings.ToLower(mediaType)
	if !strings.HasPrefix(mediaType, "image/") {
		return "", errors.New("remote content is not image/*")
	}
	return mediaType, nil
}

// validateContentModerationImageBytes accepts nonempty content sniffed as any
// image subtype. A declared SVG also accepts an SVG or XML marker within the
// first 512 bytes; this check does not decode the full image or require
// equality between declared and detected subtypes.
func validateContentModerationImageBytes(data []byte, declaredMIME string) error {
	if len(data) == 0 {
		return errors.New("image content is empty")
	}
	detected := strings.ToLower(http.DetectContentType(data))
	if strings.HasPrefix(detected, "image/") {
		return nil
	}
	// SVG can be detected as text or XML by the standard sniffer; accept its
	// declared subtype when the short prefix contains an SVG or XML marker.
	if declaredMIME == "image/svg+xml" && (strings.Contains(string(data[:min(len(data), 512)]), "<svg") || strings.Contains(string(data[:min(len(data), 512)]), "<?xml")) {
		return nil
	}
	return errors.New("image content does not match its MIME type")
}

// newContentModerationSnapshotClient constructs a direct HTTP client for
// untrusted image references. Its dial callback resolves the target again and
// dials the resulting IP literal, so an intervening host lookup cannot redirect
// that connection through an unchecked address or an environment proxy.
//
// Every returned DNS address must pass the listed private/local address checks
// before any is dialed. Failed dials try the remaining accepted addresses;
// redirects repeat URL checks and stop when five previous requests are
// recorded. Request contexts and the fifteen-second client budget control each
// exchange, and fetchContentModerationImage owns idle-connection cleanup.
func newContentModerationSnapshotClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		// Dial validated IP literals directly because an environment proxy can resolve
		// the hostname independently and reach an address that this transport has not checked.
		Proxy: nil,
		DialContext: func(ctx context.Context, network string, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := resolveContentModerationPublicIPs(ctx, host)
			if err != nil {
				return nil, err
			}
			var lastErr error
			// Try remaining checked DNS addresses after a dial failure, keeping every
			// attempt tied to the request context and the dialer timeout policy.
			for _, ip := range ips {
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   contentModerationSnapshotTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Limit successive image locations before another redirect request.
			if len(via) >= 5 {
				return errors.New("too many image redirects")
			}
			return validateContentModerationRemoteURL(req.Context(), req.URL)
		},
	}
}

// validateContentModerationRemoteURL accepts an HTTP(S) URL with a nonblank
// hostname after checking every resolved address. Nil URLs, other schemes,
// missing hosts, and any address rejected by resolveContentModerationPublicIPs
// produce an error.
func validateContentModerationRemoteURL(ctx context.Context, value *url.URL) error {
	if value == nil || (value.Scheme != "http" && value.Scheme != "https") || strings.TrimSpace(value.Hostname()) == "" {
		return errors.New("invalid remote image URL")
	}
	_, err := resolveContentModerationPublicIPs(ctx, value.Hostname())
	return err
}

// resolveContentModerationPublicIPs resolves a host using the supplied context
// and retains the resolver order. Empty replies and any address rejected by
// isContentModerationPublicIP fail the entire lookup, including mixed allowed
// and rejected address replies.
func resolveContentModerationPublicIPs(ctx context.Context, host string) ([]net.IP, error) {
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve image host: %w", err)
	}
	if len(addresses) == 0 {
		return nil, errors.New("image host has no available addresses")
	}
	ips := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		ip := address.IP
		if !isContentModerationPublicIP(ip) {
			return nil, errors.New("private or local image addresses are not allowed")
		}
		ips = append(ips, ip)
	}
	return ips, nil
}

// isContentModerationPublicIP reports whether an address passes the snapshot
// transport exclusion list. It rejects nil, private, loopback, link-local,
// unspecified, and multicast addresses; other address classes pass this
// predicate.
func isContentModerationPublicIP(ip net.IP) bool {
	if ip == nil || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	return true
}
