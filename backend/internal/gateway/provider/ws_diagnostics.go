package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"

	coderws "github.com/coder/websocket"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	upstreamopenai "github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// WebSocket diagnostics retain selected event, header, close, and payload-size
// evidence while bounding common log values. Event sampling keeps initial and
// periodic observations, with terminal and error events retained on the
// ordinary event path; buffered-event sampling follows its separate allowance.
//
// Payload summaries count recognized text and image URL bytes without
// downloading content. Recursive size estimates use scalar heuristics and
// bounded traversal, marking unavailable values with -1 rather than promising
// actual wire size. Normalized tokens can abbreviate substrings, byte
// truncation can split UTF-8, and ellipses extend the output beyond the
// selected prefix. Error summaries prefer structured context/network/close
// evidence before message matching.

// These limits bound diagnostic field bytes, initial/periodic event samples,
// and recursive payload estimates so WS logging retains selected evidence during large turns.
const (
	OpenAIWSLogValueMaxLen              = 160       // diagnostic string bytes
	OpenAIWSHeaderValueMaxLen           = 120       // diagnostic header bytes
	OpenAIWSIDValueMaxLen               = 64        // diagnostic identifier bytes
	openAIWSEventLogHeadLimit           = 20        // initial stream event allowance
	openAIWSEventLogEveryN              = 50        // later event sampling interval
	openAIWSBufferLogHeadLimit          = 8         // initial buffered event allowance
	openAIWSBufferLogEveryN             = 20        // later buffer sampling interval
	openAIWSPayloadSizeEstimateDepth    = 3         // available recursive estimate depth
	openAIWSPayloadSizeEstimateMaxBytes = 64 * 1024 // container estimate byte allowance
	openAIWSPayloadSizeEstimateMaxItems = 16        // inspected container item allowance
)

// openAIWSLogValueReplacer abbreviates recurring diagnostic words during WS
// log normalization, applying replacements to substrings before optional byte truncation.
var openAIWSLogValueReplacer = strings.NewReplacer(
	"error", "err",
	"fallback", "fb",
	"warning", "warnx",
	"failed", "fail",
)

// WarnReasoningCacheFailure records a cache-write failure with its reasoning
// item ID.
func WarnReasoningCacheFailure(itemID string, err error) {
	logging.L().Warn("openai responses chat fallback: cache reasoning content failed", zap.Error(err), zap.String("item_id", itemID))
}

// NormalizeOpenAIWSLogValue trims a log value and applies the diagnostic token
// replacer. Empty text becomes a dash placeholder; substitution can change
// words inside larger strings, so this is diagnostic text normalization.
func NormalizeOpenAIWSLogValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "-"
	}
	return openAIWSLogValueReplacer.Replace(trimmed)
}

// TruncateOpenAIWSLogValue normalizes diagnostic text and then byte-truncates
// it with an ellipsis. Nonpositive maxLen disables truncation, and the byte
// cut can split UTF-8; the appended ellipsis is additional output.
func TruncateOpenAIWSLogValue(value string, maxLen int) string {
	normalized := NormalizeOpenAIWSLogValue(value)
	if normalized == "-" || maxLen <= 0 {
		return normalized
	}
	if len(normalized) <= maxLen {
		return normalized
	}
	// The cap applies before the diagnostic ellipsis is appended.
	return normalized[:maxLen] + "..."
}

// OpenAIWSHeaderValueForLog returns a normalized bounded header value for
// diagnostics. Nil headers produce the dash placeholder, and the fixed byte
// cap is applied before adding any truncation ellipsis.
func OpenAIWSHeaderValueForLog(headers http.Header, key string) string {
	if headers == nil {
		return "-"
	}
	return TruncateOpenAIWSLogValue(headers.Get(key), OpenAIWSHeaderValueMaxLen)
}

// HasOpenAIWSHeader reports whether the requested header contains nonblank
// trimmed text.
func HasOpenAIWSHeader(headers http.Header, key string) bool {
	if headers == nil {
		return false
	}
	return strings.TrimSpace(headers.Get(key)) != ""
}

// ShouldLogOpenAIWSEvent retains initial events, periodic later events, and
// every error or terminal event. The supplied index is compared directly, so
// callers provide the indexing convention used by their stream loop.
func ShouldLogOpenAIWSEvent(idx int, eventType string) bool {
	// Retain startup evidence before periodic sampling reduces routine event logs.
	if idx <= openAIWSEventLogHeadLimit {
		return true
	}
	// Periodic samples keep later stream progress visible within the log allowance.
	if openAIWSEventLogEveryN > 0 && idx%openAIWSEventLogEveryN == 0 {
		return true
	}
	if eventType == "error" || protocolopenai.IsWSTerminalEvent(eventType) {
		return true
	}
	return false
}

// ShouldLogOpenAIWSBufferedEvent retains the initial buffered events and
// periodic later observations. The supplied index is compared directly to the
// head allowance and sampling modulus, with no event-type override.
func ShouldLogOpenAIWSBufferedEvent(idx int) bool {
	// Buffered startup events retain their own allowance before periodic sampling.
	if idx <= openAIWSBufferLogHeadLimit {
		return true
	}
	// Later buffer samples expose continued activity while limiting routine logs.
	if openAIWSBufferLogEveryN > 0 && idx%openAIWSBufferLogEveryN == 0 {
		return true
	}
	return false
}

// SummarizeOpenAIWSErrorEventFieldsFromRaw normalizes and byte-bounds the
// supplied error code, type, and message for logging. Missing values become
// dash placeholders, and each field receives its own diagnostic cap.
func SummarizeOpenAIWSErrorEventFieldsFromRaw(codeRaw, errTypeRaw, errMessageRaw string) (code string, errType string, errMessage string) {
	code = TruncateOpenAIWSLogValue(codeRaw, OpenAIWSLogValueMaxLen)
	errType = TruncateOpenAIWSLogValue(errTypeRaw, OpenAIWSLogValueMaxLen)
	errMessage = TruncateOpenAIWSLogValue(errMessageRaw, OpenAIWSLogValueMaxLen)
	return code, errType, errMessage
}

// SummarizeOpenAIWSPayloadKeySizes formats the largest estimated payload
// values in descending size and then lexical key order. Empty input returns a
// dash, and nonpositive or oversized topN includes every key.
//
// Each value is estimated independently with bounded depth, item count, and
// bytes; an unavailable estimate is represented by -1 and sorts below
// nonnegative values. Ranking uses that heuristic result, not serialized
// request size. Callers use this summary to locate large fields while keeping
// the actual payload out of these diagnostics.
func SummarizeOpenAIWSPayloadKeySizes(payload map[string]any, topN int) string {
	if len(payload) == 0 {
		return "-"
	}
	// keySize pairs one payload key with its independent diagnostic size estimate.
	type keySize struct {
		Key  string // source payload field name
		Size int    // estimated bytes or unavailable
	}
	sizes := make([]keySize, 0, len(payload))
	for key, value := range payload {
		size := estimateOpenAIWSPayloadValueSize(value, openAIWSPayloadSizeEstimateDepth)
		sizes = append(sizes, keySize{Key: key, Size: size})
	}
	sort.Slice(sizes, func(i, j int) bool {
		if sizes[i].Size == sizes[j].Size {
			return sizes[i].Key < sizes[j].Key
		}
		return sizes[i].Size > sizes[j].Size
	})

	if topN <= 0 || topN > len(sizes) {
		topN = len(sizes)
	}
	parts := make([]string, 0, topN)
	for idx := range topN {
		item := sizes[idx]
		parts = append(parts, fmt.Sprintf("%s:%d", item.Key, item.Size))
	}
	return strings.Join(parts, ",")
}

// estimateOpenAIWSPayloadValueSize returns a bounded diagnostic size
// approximation, with -1 for exhausted traversal limits or failed fallback
// serialization. Callers supply the available depth, and nil/string/bytes plus
// scalar primitives use direct heuristic sizes.
//
// Maps and slices add structural overhead to child estimates, rejecting the
// whole estimate when a child is unavailable or the container exceeds its
// item/byte bounds. Other values use JSON serialization with the byte cap.
// These rules do not guarantee encoded wire length, and map iteration can
// affect which limit is reached first.
func estimateOpenAIWSPayloadValueSize(value any, depth int) int {
	if depth <= 0 {
		return -1
	}
	switch v := value.(type) {
	case nil:
		// A nil interface value stays in field ranking with zero content size;
		// this heuristic omits the JSON null literal that wire encoding would add.
		return 0
	case string:
		// Count source bytes; JSON quotes and escaped-character expansion are
		// outside this text-field estimate used for relative diagnostic ranking.
		return len(v)
	case []byte:
		// Raw length omits Base64 cost for nonnil JSON byte slices. Nil slices
		// contribute zero here despite JSON's null spelling, so this size locates
		// retained data and cannot budget an encoded request.
		return len(v)
	case bool:
		// Boolean estimates ignore true/false JSON literal length; their fixed
		// contribution supports relative field diagnosis, not encoded byte accounting.
		return 1
	// Integer and floating branches use the same fixed diagnostic width.
	// Magnitude, decimal spelling, and exponent length do not affect their
	// estimated contribution, so these values cannot predict serialized numbers.
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return 8
	case float32, float64:
		return 8
	case map[string]any:
		// Empty and nonempty map estimates start with two wrapper bytes. Nil maps
		// use that same estimate, which omits their JSON null spelling.
		if len(v) == 0 {
			return 2
		}
		total := 2
		count := 0
		for key, item := range v {
			count++
			// Exceeding the item allowance discards this map's partial sum; a prefix
			// traversal cannot represent the whole field in diagnostic ranking.
			if count > openAIWSPayloadSizeEstimateMaxItems {
				return -1
			}
			itemSize := estimateOpenAIWSPayloadValueSize(item, depth-1)
			if itemSize < 0 {
				return -1
			}
			// Three structural bytes account for key quotes and a colon. Entry
			// separators and encoded key-escape expansion are outside this sum;
			// child estimates retain their own representation omissions.
			total += len(key) + itemSize + 3
			// Discard the container estimate beyond the byte allowance. Its -1 result
			// ranks below available estimates rather than ranking a partial sum.
			if total > openAIWSPayloadSizeEstimateMaxBytes {
				return -1
			}
		}
		return total
	case []any:
		// Empty and nonempty slices use the array-wrapper estimate. Nil slices
		// receive that same size even though JSON represents them as null.
		if len(v) == 0 {
			return 2
		}
		total := 2
		limit := len(v)
		// Reject oversized arrays before reading their elements, so a traversed
		// prefix cannot be ranked as the size of the complete field.
		if limit > openAIWSPayloadSizeEstimateMaxItems {
			return -1
		}
		for i := range limit {
			itemSize := estimateOpenAIWSPayloadValueSize(v[i], depth-1)
			if itemSize < 0 {
				return -1
			}
			// Per-child overhead includes the final element, so JSON's omission of
			// a separator after that element is outside this diagnostic estimate.
			total += itemSize + 1
			// Discard the container estimate beyond the byte allowance. Its -1 result
			// ranks below available estimates rather than ranking a partial sum.
			if total > openAIWSPayloadSizeEstimateMaxBytes {
				return -1
			}
		}
		return total
	default:
		// Unknown value representations use JSON size when encoding succeeds;
		// serialization failure yields unavailable, leaving other payload keys independently estimable.
		raw, err := json.Marshal(v)
		if err != nil {
			return -1
		}
		// Oversized serialized fallback values also rank below available estimates.
		if len(raw) > openAIWSPayloadSizeEstimateMaxBytes {
			return -1
		}
		return len(raw)
	}
}

// extractOpenAIWSImageURL returns trimmed image URL text from a string or
// object url field. Unsupported shapes return empty, allowing input summaries
// to skip entries lacking a recognized URL representation.
func extractOpenAIWSImageURL(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		if raw, ok := v["url"].(string); ok {
			return strings.TrimSpace(raw)
		}
	}
	return ""
}

// SummarizeOpenAIWSInput reports item count, recognized text bytes, and
// data/remote image URL observations for decoded array input. Non-array or
// empty input returns a dash; malformed item shapes are skipped.
//
// A content array is inspected before direct item type, avoiding
// double-counting the same item through both representations. Text counters
// and data-URL length counters measure bytes despite their historical chars
// labels. Remote URLs are counted without fetching content, and the original
// array length includes unrecognized items so diagnostics retain the supplied
// input size.
func SummarizeOpenAIWSInput(input any) string {
	items, ok := input.([]any)
	if !ok || len(items) == 0 {
		return "-"
	}

	itemCount := len(items)
	textChars := 0
	imageDataURLs := 0
	imageDataURLChars := 0
	imageRemoteURLs := 0

	handleContentItem := func(contentItem map[string]any) {
		contentType, _ := contentItem["type"].(string)
		switch strings.TrimSpace(contentType) {
		case "input_text", "output_text", "text":
			if text, ok := contentItem["text"].(string); ok {
				textChars += len(text)
			}
		case "input_image":
			imageURL := extractOpenAIWSImageURL(contentItem["image_url"])
			if imageURL == "" {
				return
			}
			if strings.HasPrefix(strings.ToLower(imageURL), "data:image/") {
				imageDataURLs++
				imageDataURLChars += len(imageURL)
				return
			}
			imageRemoteURLs++
		}
	}

	handleInputItem := func(inputItem map[string]any) {
		// An item with structured content is counted through those parts first;
		// return afterward so its direct type cannot count the same text or image twice.
		if content, ok := inputItem["content"].([]any); ok {
			for _, rawContent := range content {
				contentItem, ok := rawContent.(map[string]any)
				if !ok {
					continue
				}
				handleContentItem(contentItem)
			}
			return
		}

		itemType, _ := inputItem["type"].(string)
		switch strings.TrimSpace(itemType) {
		case "input_text", "output_text", "text":
			if text, ok := inputItem["text"].(string); ok {
				textChars += len(text)
			}
		case "input_image":
			imageURL := extractOpenAIWSImageURL(inputItem["image_url"])
			if imageURL == "" {
				return
			}
			if strings.HasPrefix(strings.ToLower(imageURL), "data:image/") {
				imageDataURLs++
				imageDataURLChars += len(imageURL)
				return
			}
			imageRemoteURLs++
		}
	}

	for _, rawItem := range items {
		inputItem, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		handleInputItem(inputItem)
	}

	return fmt.Sprintf(
		"items=%d,text_chars=%d,image_data_urls=%d,image_data_url_chars=%d,image_remote_urls=%d",
		itemCount,
		textChars,
		imageDataURLs,
		imageDataURLChars,
		imageRemoteURLs,
	)
}

// LogOpenAIWSModeInfo writes a formatted message under the OpenAI WS-mode
// component marker.
func LogOpenAIWSModeInfo(format string, args ...any) {
	logging.LegacyPrintf("service.openai_gateway", "[OpenAI WS Mode][openai_ws_mode=true] "+format, args...)
}

// IsOpenAIWSModeDebugEnabled reports whether the current logger accepts
// debug-level entries.
func IsOpenAIWSModeDebugEnabled() bool {
	return logging.L().Core().Enabled(zap.DebugLevel)
}

// LogOpenAIWSModeDebug writes the WS-mode debug marker when the current logger
// enables DebugLevel. Disabled debug returns immediately, leaving message
// emission to the same legacy logging component used by other WS diagnostics.
func LogOpenAIWSModeDebug(format string, args ...any) {
	if !IsOpenAIWSModeDebugEnabled() {
		return
	}
	logging.LegacyPrintf("service.openai_gateway", "[debug] [OpenAI WS Mode][openai_ws_mode=true] "+format, args...)
}

// LogOpenAIWSBindResponseProviderWarn records a failed response-to-provider
// binding with group and provider IDs. Nil errors are ignored, and response
// identity is normalized and byte-bounded for the diagnostic entry.
func LogOpenAIWSBindResponseProviderWarn(groupID, providerID int64, responseID string, err error) {
	if err == nil {
		return
	}
	logging.L().Warn(
		"openai.ws_bind_response_provider_failed",
		zap.Int64("group_id", groupID),
		zap.Int64("provider_id", providerID),
		zap.String("response_id", TruncateOpenAIWSLogValue(responseID, OpenAIWSIDValueMaxLen)),
		zap.Error(err),
	)
}

// SummarizeOpenAIWSReadCloseError extracts a websocket close status and reason
// from the error chain. Missing close status returns dash placeholders; a
// recognized status is formatted and normalized, with a reason from CloseError
// when present.
func SummarizeOpenAIWSReadCloseError(err error) (status string, reason string) {
	if err == nil {
		return "-", "-"
	}
	statusCode := coderws.CloseStatus(err)
	if statusCode == -1 {
		return "-", "-"
	}
	closeStatus := fmt.Sprintf("%d(%s)", int(statusCode), statusCode.String())
	closeReason := "-"
	if closeErr, ok := errors.AsType[coderws.CloseError](err); ok {
		reasonText := strings.TrimSpace(closeErr.Reason)
		if reasonText != "" {
			closeReason = NormalizeOpenAIWSLogValue(reasonText)
		}
	}
	return NormalizeOpenAIWSLogValue(closeStatus), closeReason
}

// unwrapOpenAIWSDialBaseError exposes a nonnil WSDialError cause when
// available, otherwise the supplied error. Nil input remains nil, letting
// callers share classification across wrapped and ordinary dial failures.
func unwrapOpenAIWSDialBaseError(err error) error {
	if err == nil {
		return nil
	}
	var dialErr *upstreamopenai.WSDialError
	if errors.As(err, &dialErr) && dialErr != nil && dialErr.Err != nil {
		return dialErr.Err
	}
	return err
}

// openAIWSDialRespHeaderForLog extracts a response header from a recognized
// WSDialError and applies its diagnostic cap. Missing error metadata or
// headers returns a dash, keeping absent handshake evidence distinguishable.
func openAIWSDialRespHeaderForLog(err error, key string) string {
	var dialErr *upstreamopenai.WSDialError
	if !errors.As(err, &dialErr) || dialErr == nil || dialErr.ResponseHeaders == nil {
		return "-"
	}
	return TruncateOpenAIWSLogValue(dialErr.ResponseHeaders.Get(key), OpenAIWSHeaderValueMaxLen)
}

// classifyOpenAIWSDialError classifies the underlying dial cause for
// diagnostic tokens. Context, network timeout, and websocket close evidence
// precede message-pattern matching; unknown nonnil causes return dial_error
// and nil causes return a dash.
func classifyOpenAIWSDialError(err error) string {
	if err == nil {
		return "-"
	}
	baseErr := unwrapOpenAIWSDialBaseError(err)
	if baseErr == nil {
		return "-"
	}
	if errors.Is(baseErr, context.DeadlineExceeded) {
		return "ctx_deadline_exceeded"
	}
	if errors.Is(baseErr, context.Canceled) {
		return "ctx_canceled"
	}
	var netErr net.Error
	if errors.As(baseErr, &netErr) && netErr.Timeout() {
		return "net_timeout"
	}
	if status := coderws.CloseStatus(baseErr); status != -1 {
		return NormalizeOpenAIWSLogValue(fmt.Sprintf("ws_close_%d", int(status)))
	}
	message := strings.ToLower(strings.TrimSpace(baseErr.Error()))
	switch {
	case strings.Contains(message, "handshake not finished"):
		return "handshake_not_finished"
	case strings.Contains(message, "bad handshake"):
		return "bad_handshake"
	case strings.Contains(message, "connection refused"):
		return "connection_refused"
	case strings.Contains(message, "no such host"):
		return "dns_not_found"
	case strings.Contains(message, "tls"):
		return "tls_error"
	case strings.Contains(message, "i/o timeout"):
		return "io_timeout"
	case strings.Contains(message, "context deadline exceeded"):
		return "ctx_deadline_exceeded"
	default:
		return "dial_error"
	}
}

// SummarizeOpenAIWSDialError combines status, bounded handshake headers, dial
// classification, and close observations. Absent evidence uses dash
// placeholders and zero status; structured WSDialError data supplies headers
// while its underlying cause supplies classification.
func SummarizeOpenAIWSDialError(err error) (
	statusCode int,
	dialClass string,
	closeStatus string,
	closeReason string,
	respServer string,
	respVia string,
	respCFRay string,
	respRequestID string,
) {
	dialClass = "-"
	closeStatus = "-"
	closeReason = "-"
	respServer = "-"
	respVia = "-"
	respCFRay = "-"
	respRequestID = "-"
	if err == nil {
		return
	}
	var dialErr *upstreamopenai.WSDialError
	if errors.As(err, &dialErr) && dialErr != nil {
		statusCode = dialErr.StatusCode
		respServer = openAIWSDialRespHeaderForLog(err, "server")
		respVia = openAIWSDialRespHeaderForLog(err, "via")
		respCFRay = openAIWSDialRespHeaderForLog(err, "cf-ray")
		respRequestID = openAIWSDialRespHeaderForLog(err, "x-request-id")
	}
	dialClass = NormalizeOpenAIWSLogValue(classifyOpenAIWSDialError(err))
	closeStatus, closeReason = SummarizeOpenAIWSReadCloseError(unwrapOpenAIWSDialBaseError(err))
	return
}

// IsOpenAIWSClientDisconnectError recognizes EOF, closed/cancelled contexts,
// selected close statuses, and common socket termination text. Nil and
// unmatched errors return false; this classifier describes disconnect evidence
// rather than provider recovery eligibility.
func IsOpenAIWSClientDisconnectError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) {
		return true
	}
	switch coderws.CloseStatus(err) {
	case coderws.StatusNormalClosure, coderws.StatusGoingAway, coderws.StatusNoStatusRcvd, coderws.StatusAbnormalClosure:
		return true
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	if message == "" {
		return false
	}
	return strings.Contains(message, "failed to read frame header: eof") ||
		strings.Contains(message, "unexpected eof") ||
		strings.Contains(message, "use of closed network connection") ||
		strings.Contains(message, "connection reset by peer") ||
		strings.Contains(message, "broken pipe") ||
		strings.Contains(message, "an existing connection was forcibly closed by the remote host") ||
		strings.Contains(message, "an established connection was aborted")
}

// ClassifyOpenAIWSReadFallbackReason names policy-violation and
// oversized-message websocket closes for recovery diagnostics. Nil and other
// errors use read_event, allowing callers to retain one default reason for
// ordinary read failures.
func ClassifyOpenAIWSReadFallbackReason(err error) string {
	if err == nil {
		return "read_event"
	}
	switch coderws.CloseStatus(err) {
	case coderws.StatusPolicyViolation:
		return "policy_violation"
	case coderws.StatusMessageTooBig:
		return "message_too_big"
	default:
		return "read_event"
	}
}

// SortedOpenAIWSPayloadKeys returns payload keys in a new lexically sorted
// slice, or nil for an empty map. Callers keep the map stable during enumeration;
// modifying returned entries leaves its keys unchanged.
func SortedOpenAIWSPayloadKeys(m map[string]any) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
