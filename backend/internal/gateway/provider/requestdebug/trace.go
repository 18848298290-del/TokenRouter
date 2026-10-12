package requestdebug

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	claude "github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

const debugGatewayBodyDefaultFilename = "gateway_debug.log"

// Trace is shared by the app. Callers close it after requests finish; repeated Close calls return the same result.
type Trace struct {
	file      *os.File
	mimic     bool
	closeOnce sync.Once
	closeErr  error
}

func New(path, mimic string) *Trace {
	trace := &Trace{mimic: parseDebugEnvBool(mimic)}
	if path = strings.TrimSpace(path); path != "" {
		trace.open(path)
	}
	return trace
}

func (s *Trace) Close() error {
	s.closeOnce.Do(func() {
		if s.file != nil {
			s.closeErr = s.file.Close()
		}
	})
	return s.closeErr
}

// Capture inspects the request when diagnostics are retained or mimic logging is enabled.
func (s *Trace) Capture(req *http.Request, body []byte, target *gatewayprovider.ExecutionProvider, tokenType string, mimic, retain bool) string {
	if !retain && (s == nil || !s.mimic) {
		return ""
	}
	line := CaptureLine(req, body, target, tokenType, mimic)
	if s != nil && s.mimic && line != "" {
		logging.LegacyPrintf("service.gateway", "[ClaudeMimicDebug] %s", line)
	}
	return line
}

func parseDebugEnvBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func redactAuthHeaderValue(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	// Keep the Bearer scheme visible while redacting credentials.
	if strings.HasPrefix(strings.ToLower(v), "bearer ") {
		return "Bearer [redacted]"
	}
	return "[redacted]"
}

func safeHeaderValueForLog(key string, v string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	switch key {
	case "authorization", "x-api-key":
		return redactAuthHeaderValue(v)
	default:
		return strings.TrimSpace(v)
	}
}

func extractSystemPreviewFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	sys := gjson.GetBytes(body, "system")
	if !sys.Exists() {
		return ""
	}

	switch {
	case sys.IsArray():
		for _, item := range sys.Array() {
			if !item.IsObject() {
				continue
			}
			if strings.EqualFold(item.Get("type").String(), "text") {
				if t := item.Get("text").String(); strings.TrimSpace(t) != "" {
					return t
				}
			}
		}
		return ""
	case sys.Type == gjson.String:
		return sys.String()
	default:
		return ""
	}
}

func CaptureLine(req *http.Request, body []byte, provider *gatewayprovider.ExecutionProvider, tokenType string, mimicClaudeCode bool) string {
	if req == nil {
		return ""
	}

	// Capture fingerprint headers with redacted authentication values.
	interesting := []string{
		"user-agent",
		"x-app",
		"anthropic-dangerous-direct-browser-access",
		"anthropic-version",
		"anthropic-beta",
		"x-stainless-lang",
		"x-stainless-package-version",
		"x-stainless-os",
		"x-stainless-arch",
		"x-stainless-runtime",
		"x-stainless-runtime-version",
		"x-stainless-retry-count",
		"x-stainless-timeout",
		"authorization",
		"x-api-key",
		"content-type",
		"accept",
		"x-stainless-helper-method",
	}

	h := make([]string, 0, len(interesting))
	for _, k := range interesting {
		if v := req.Header.Get(k); v != "" {
			h = append(h, fmt.Sprintf("%s=%q", k, safeHeaderValueForLog(k, v)))
		}
	}

	metaUserID := strings.TrimSpace(gjson.GetBytes(body, "metadata.user_id").String())
	sysPreview := strings.TrimSpace(extractSystemPreviewFromBody(body))

	// Limit the system prompt preview to 300 bytes before escaping line breaks.
	if len(sysPreview) > 300 {
		sysPreview = sysPreview[:300] + "..."
	}
	sysPreview = strings.ReplaceAll(sysPreview, "\n", "\\n")
	sysPreview = strings.ReplaceAll(sysPreview, "\r", "\\r")

	aid := int64(0)
	aname := ""
	if provider != nil {
		aid = provider.Record.ID
		aname = provider.Record.Name
	}

	return fmt.Sprintf(
		"url=%s provider=%d(%s) tokenType=%s mimic=%t meta.user_id=%q system.preview=%q headers={%s}",
		req.URL.String(),
		aid,
		aname,
		tokenType,
		mimicClaudeCode,
		metaUserID,
		sysPreview,
		strings.Join(h, " "),
	)
}

func (s *Trace) open(path string) {
	if parseDebugEnvBool(path) {
		path = debugGatewayBodyDefaultFilename
	}

	// An existing directory uses the default log filename.

	if info, err := os.Stat(path); err == nil && info.IsDir() {
		path = filepath.Join(path, debugGatewayBodyDefaultFilename)
	}

	// Create the parent directory if needed.
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			slog.Error("failed to create gateway debug log directory", "dir", dir, "error", err)
			return
		}
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		slog.Error("failed to open gateway debug log file", "path", path, "error", err)
		return
	}
	s.file = f
	slog.Info("gateway debug logging enabled", "path", path)
}

// Snapshot writes headers and the full body to the debug log for comparison
// between client input and upstream requests. Authentication headers are redacted.
//
// Enable with an environment variable:
//
//	TOKENROUTER_DEBUG_GATEWAY_BODY=1                      # Use gateway_debug.log
//	TOKENROUTER_DEBUG_GATEWAY_BODY=/tmp/gateway_debug.log # Use the specified path
//
// Tags identify the capture point: "CLIENT_ORIGINAL" or "UPSTREAM_FORWARD".
func (s *Trace) Snapshot(tag string, headers http.Header, body []byte, extra map[string]string) {
	f := s.file
	if f == nil {
		return
	}

	var buf strings.Builder
	ts := time.Now().Format("2006-01-02 15:04:05.000")
	fmt.Fprintf(&buf, "\n========== [%s] %s ==========\n", ts, tag)

	// Sort context keys for repeatable output.
	if len(extra) > 0 {
		fmt.Fprint(&buf, "--- context ---\n")
		extraKeys := make([]string, 0, len(extra))
		for k := range extra {
			extraKeys = append(extraKeys, k)
		}
		sort.Strings(extraKeys)
		for _, k := range extraKeys {
			fmt.Fprintf(&buf, "  %s: %s\n", k, extra[k])
		}
	}

	// Use Claude CLI header order for packet-capture comparisons and redact authentication.
	fmt.Fprint(&buf, "--- headers ---\n")
	for _, k := range claude.SortHeadersByWireOrder(headers) {
		for _, v := range headers[k] {
			fmt.Fprintf(&buf, "  %s: %s\n", k, safeHeaderValueForLog(k, v))
		}
	}

	// Pretty-print the full JSON body for comparison.
	fmt.Fprint(&buf, "--- body ---\n")
	if len(body) == 0 {
		fmt.Fprint(&buf, "  (empty)\n")
	} else {
		var pretty bytes.Buffer
		if json.Indent(&pretty, body, "  ", "  ") == nil {
			fmt.Fprintf(&buf, "  %s\n", pretty.Bytes())
		} else {
			// Write the raw body when JSON formatting fails.
			fmt.Fprintf(&buf, "  %s\n", body)
		}
	}

	// Concurrent snapshots may interleave in the debug file.
	_, _ = f.WriteString(buf.String())
}
