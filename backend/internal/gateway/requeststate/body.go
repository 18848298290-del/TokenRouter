package requeststate

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unsafe"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

const claudeCodeLongContextModelSuffix = "[1m]"

var (
	sessionUserAgentProductPattern = regexp.MustCompile(`([A-Za-z0-9._-]+)/[A-Za-z0-9._-]+`)
	sessionUserAgentVersionPattern = regexp.MustCompile(`\bv?\d+(?:\.\d+){1,3}\b`)
)

// SessionContext identifies the request source for session hashing.
// GenerateSessionHash mixes it into content-based fallback hashes to separate identical messages from different users.
type SessionContext struct {
	ClientIP  string
	UserAgent string
	APIKeyID  int64
}

type jsonRange struct {
	start int        // Inclusive byte offset in the current body.
	end   int        // Exclusive byte offset in the current body.
	kind  gjson.Type // JSON value type for callers' type checks.
}

type RequestBodyRef struct {
	data []byte
}

// ParsedRequest holds fields and byte ranges parsed from a gateway request.
// Execution and session hashing share model, stream, messages, and metadata parsed at entry.
type ParsedRequest struct {
	Body            *RequestBodyRef // Body reference used for forwarding; update through ReplaceBody.
	Model           string          // Requested model name.
	Stream          bool            // Whether the request streams.
	MetadataUserID  string          // metadata.user_id used for session affinity.
	HasSystem       bool            // Whether system is present, including null.
	ThinkingEnabled bool            // Thinking enabled or adaptive; may affect the upstream model.
	OutputEffort    string          // Claude output_config.effort.
	MaxTokens       int             // max_tokens used to identify probe requests.
	SessionContext  *SessionContext // Optional request-source factors for session hashing.

	protocol      string    // Body protocol used when refreshing byte ranges.
	systemRange   jsonRange // system or systemInstruction.parts range in the current Body.
	messagesRange jsonRange // messages or contents range in the current Body.
	inputRange    jsonRange // Responses input range in the current Body.

	// GroupID is the request group selected through the API key.
	GroupID *int64

	// OnUpstreamAccepted releases the serial lock after upstream acceptance.
	// Streaming callers invoke it after receiving 2xx response headers.
	OnUpstreamAccepted func()
}

func NewRequestBodyRef(data []byte) *RequestBodyRef {
	return &RequestBodyRef{data: data}
}

func (b *RequestBodyRef) Bytes() []byte {
	if b == nil {
		return nil
	}
	return b.data
}

func (b *RequestBodyRef) Len() int {
	if b == nil {
		return 0
	}
	return len(b.data)
}

func (b *RequestBodyRef) Replace(data []byte) {
	if b == nil {
		return
	}
	b.data = data
}

func missingJSONRange() jsonRange {
	return jsonRange{start: -1, end: -1}
}

func rangeFromResult(r gjson.Result) jsonRange {
	if r.Raw == "" || r.Index <= 0 {
		return missingJSONRange()
	}
	end := r.Index + len(r.Raw)
	if end < r.Index {
		return missingJSONRange()
	}
	return jsonRange{start: r.Index, end: end, kind: r.Type}
}

func (r jsonRange) exists() bool {
	return r.start >= 0 && r.end >= r.start
}

// clearGatewayRequestDerivedState clears body-derived fields before reparsing a replacement body.
func clearGatewayRequestDerivedState(parsed *ParsedRequest) {
	if parsed == nil {
		return
	}
	parsed.Model = ""
	parsed.Stream = false
	parsed.MetadataUserID = ""
	parsed.HasSystem = false
	parsed.ThinkingEnabled = false
	parsed.OutputEffort = ""
	parsed.MaxTokens = 0
	parsed.systemRange = missingJSONRange()
	parsed.messagesRange = missingJSONRange()
	parsed.inputRange = missingJSONRange()
}

func clearGatewayRequestRanges(parsed *ParsedRequest) {
	if parsed == nil {
		return
	}
	parsed.HasSystem = false
	parsed.systemRange = missingJSONRange()
	parsed.messagesRange = missingJSONRange()
	parsed.inputRange = missingJSONRange()
}

func setGatewayRequestRanges(parsed *ParsedRequest, protocol string, jsonStr string) {
	if parsed == nil {
		return
	}
	switch protocol {
	case capability.PlatformGemini:
		if sysParts := gjson.Get(jsonStr, "systemInstruction.parts"); sysParts.Exists() && sysParts.IsArray() {
			parsed.systemRange = rangeFromResult(sysParts)
		}
		if contents := gjson.Get(jsonStr, "contents"); contents.Exists() && contents.IsArray() {
			parsed.messagesRange = rangeFromResult(contents)
		}
	default:
		if sys := gjson.Get(jsonStr, "system"); sys.Exists() {
			parsed.HasSystem = true
			parsed.systemRange = rangeFromResult(sys)
		}
		if msgs := gjson.Get(jsonStr, "messages"); msgs.Exists() && msgs.IsArray() {
			parsed.messagesRange = rangeFromResult(msgs)
		}
		if protocol == "responses" {
			if input := gjson.Get(jsonStr, "input"); input.Exists() {
				parsed.inputRange = rangeFromResult(input)
			}
		}
	}
}

// normalizeClaudeCodeLongContextModel strips trailing Claude Code [1m] context selectors,
// including repeated suffixes, while retaining a name consisting solely of the selector.
func normalizeClaudeCodeLongContextModel(model string) string {
	for len(model) > len(claudeCodeLongContextModelSuffix) &&
		strings.EqualFold(model[len(model)-len(claudeCodeLongContextModelSuffix):], claudeCodeLongContextModelSuffix) {
		model = model[:len(model)-len(claudeCodeLongContextModelSuffix)]
	}
	return model
}

// parseGatewayRequestCurrentBody parses scalars and records raw system and message byte ranges.
func parseGatewayRequestCurrentBody(parsed *ParsedRequest, protocol string) error {
	if parsed == nil || parsed.Body == nil {
		return fmt.Errorf("empty request body")
	}

	bodyBytes := parsed.Body.Bytes()
	if !gjson.ValidBytes(bodyBytes) {
		return DescribeInvalidJSON(bodyBytes)
	}

	// Read through a zero-copy string view; replacing Body requires reparsing all derived fields.
	jsonStr := *(*string)(unsafe.Pointer(&bodyBytes))
	clearGatewayRequestDerivedState(parsed)
	parsed.protocol = protocol

	modelResult := gjson.Get(jsonStr, "model")
	if modelResult.Exists() {
		if modelResult.Type != gjson.String {
			return fmt.Errorf("invalid model field type")
		}
		parsed.Model = modelResult.String()
		if protocol == capability.PlatformAnthropic {
			normalizedModel := normalizeClaudeCodeLongContextModel(parsed.Model)
			if normalizedModel != parsed.Model {
				normalizedBody, err := sjson.SetBytes(bodyBytes, "model", normalizedModel)
				if err != nil {
					return fmt.Errorf("normalize model field: %w", err)
				}
				parsed.Body.Replace(normalizedBody)
				bodyBytes = normalizedBody
				jsonStr = *(*string)(unsafe.Pointer(&bodyBytes))
				parsed.Model = normalizedModel
			}
		}
	}

	streamResult := gjson.Get(jsonStr, "stream")
	if streamResult.Exists() {
		if streamResult.Type != gjson.True && streamResult.Type != gjson.False {
			return fmt.Errorf("invalid stream field type")
		}
		parsed.Stream = streamResult.Bool()
	}

	parsed.MetadataUserID = gjson.Get(jsonStr, "metadata.user_id").String()

	thinkingType := gjson.Get(jsonStr, "thinking.type").String()
	parsed.ThinkingEnabled = thinkingType == "enabled" || thinkingType == "adaptive"

	parsed.OutputEffort = strings.TrimSpace(gjson.Get(jsonStr, "output_config.effort").String())

	maxTokensResult := gjson.Get(jsonStr, "max_tokens")
	if maxTokensResult.Exists() && maxTokensResult.Type == gjson.Number {
		f := maxTokensResult.Float()
		if !math.IsNaN(f) && !math.IsInf(f, 0) && f == math.Trunc(f) &&
			f <= float64(math.MaxInt) && f >= float64(math.MinInt) {
			parsed.MaxTokens = int(f)
		}
	}

	setGatewayRequestRanges(parsed, protocol, jsonStr)
	return nil
}

func refreshGatewayRequestRanges(parsed *ParsedRequest, protocol string) error {
	return parseGatewayRequestCurrentBody(parsed, protocol)
}

// DescribeInvalidJSON reparses a rejected body with encoding/json for diagnostic details.
// Syntax errors report length, offset, and the offending character,
// without embedding the request body in the error.
func DescribeInvalidJSON(body []byte) error {
	var raw json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		if syntaxErr, ok := errors.AsType[*json.SyntaxError](err); ok {
			return fmt.Errorf("invalid json (len=%d, offset=%d): %s", len(body), syntaxErr.Offset, syntaxErr.Error())
		}
		return fmt.Errorf("invalid json (len=%d): %w", len(body), err)
	}
	// If encoding/json accepts a body rejected by gjson, report its length alone.
	return fmt.Errorf("invalid json (len=%d)", len(body))
}

// NormalizeSessionUserAgent reduces UA noise for sticky-session and digest hashing.
// It preserves the set of product names from Product/Version tokens while
// discarding version-only changes and incidental comments.
func NormalizeSessionUserAgent(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	matches := sessionUserAgentProductPattern.FindAllStringSubmatch(raw, -1)
	if len(matches) == 0 {
		return normalizeSessionUserAgentFallback(raw)
	}

	products := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		product := strings.ToLower(strings.TrimSpace(match[1]))
		if product == "" {
			continue
		}
		if _, exists := seen[product]; exists {
			continue
		}
		seen[product] = struct{}{}
		products = append(products, product)
	}
	if len(products) == 0 {
		return normalizeSessionUserAgentFallback(raw)
	}
	sort.Strings(products)
	return strings.Join(products, "+")
}

func normalizeSessionUserAgentFallback(raw string) string {
	normalized := strings.ToLower(strings.Join(strings.Fields(raw), " "))
	normalized = sessionUserAgentVersionPattern.ReplaceAllString(normalized, "")
	return strings.Join(strings.Fields(normalized), " ")
}

// ParseGatewayRequest parses fields and raw ranges for the selected protocol.
// Gemini uses systemInstruction.parts and contents; other protocols use system and messages.
// The responses protocol also records input. Anthropic normalizes trailing [1m] model selectors.
func ParseGatewayRequest(body *RequestBodyRef, protocol string) (*ParsedRequest, error) {
	parsed := &ParsedRequest{Body: body}
	if err := parseGatewayRequestCurrentBody(parsed, protocol); err != nil {
		return nil, err
	}
	return parsed, nil
}

func (p *ParsedRequest) raw(r jsonRange) []byte {
	if p == nil || p.Body == nil || !r.exists() {
		return nil
	}
	body := p.Body.Bytes()
	if r.end > len(body) {
		return nil
	}
	return body[r.start:r.end]
}

func (p *ParsedRequest) SystemRaw() []byte {
	return p.raw(p.systemRange)
}

func (p *ParsedRequest) MessagesRaw() []byte {
	return p.raw(p.messagesRange)
}

func (p *ParsedRequest) InputRaw() []byte {
	return p.raw(p.inputRange)
}

func (p *ParsedRequest) SystemValue() (any, bool) {
	raw := p.SystemRaw()
	if len(raw) == 0 {
		return nil, false
	}
	var system any
	if err := json.Unmarshal(raw, &system); err != nil {
		return nil, false
	}
	return system, true
}

// CloneForBody creates independent parsed state around the supplied bytes; it does not copy those bytes.
func (p *ParsedRequest) CloneForBody(body []byte) (*ParsedRequest, error) {
	if p == nil {
		return nil, fmt.Errorf("parse request: empty request")
	}
	clone := *p
	clone.Body = NewRequestBodyRef(body)
	clone.OnUpstreamAccepted = nil
	if err := refreshGatewayRequestRanges(&clone, clone.protocol); err != nil {
		return nil, err
	}
	return &clone, nil
}

// ReplaceBody updates the body and reparses its fields and ranges; failures clear raw ranges.
func (p *ParsedRequest) ReplaceBody(data []byte) error {
	if p == nil {
		return fmt.Errorf("parse request: empty request")
	}
	if p.Body == nil {
		p.Body = NewRequestBodyRef(data)
	} else {
		p.Body.Replace(data)
	}
	if err := refreshGatewayRequestRanges(p, p.protocol); err != nil {
		clearGatewayRequestRanges(p)
		return err
	}
	return nil
}

// FirstNonEmpty returns the first nonempty value after trimming surrounding whitespace.
func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
