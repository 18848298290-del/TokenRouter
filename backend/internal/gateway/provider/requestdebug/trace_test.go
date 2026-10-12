package requestdebug

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCaptureOnlyForRetainedDiagnosticOrEnabledLogging checks capture gating and credential redaction.
func TestCaptureOnlyForRetainedDiagnosticOrEnabledLogging(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "https://example.test/v1/messages", nil)
	request.Header.Set("Authorization", "Bearer fixture-secret")
	trace := New("", "false")
	if line := trace.Capture(request, nil, nil, "apikey", false, false); line != "" {
		t.Fatalf("关闭诊断时不应生成快照：%s", line)
	}
	line := trace.Capture(request, nil, nil, "oauth", true, true)
	if line == "" || !strings.Contains(line, "[redacted]") || strings.Contains(line, "fixture-secret") {
		t.Fatalf("保留的诊断必须包含脱敏后的身份：%s", line)
	}
}

func TestParseDebugEnvBool(t *testing.T) {
	t.Run("empty is false", func(t *testing.T) {
		if parseDebugEnvBool("") {
			t.Fatalf("expected false for empty string")
		}
	})

	t.Run("true-like values", func(t *testing.T) {
		for _, value := range []string{"1", "true", "TRUE", "yes", "on"} {
			t.Run(value, func(t *testing.T) {
				if !parseDebugEnvBool(value) {
					t.Fatalf("expected true for %q", value)
				}
			})
		}
	})

	t.Run("false-like values", func(t *testing.T) {
		for _, value := range []string{"0", "false", "off", "debug"} {
			t.Run(value, func(t *testing.T) {
				if parseDebugEnvBool(value) {
					t.Fatalf("expected false for %q", value)
				}
			})
		}
	})
}
