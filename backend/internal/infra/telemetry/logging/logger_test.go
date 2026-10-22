package logging

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Logger fixtures replace process stdout and stderr serially, routing
// accepted messages into pipes and optional temporary files. They close pipe
// writers before reading so reads can reach EOF, then inspect severity
// routing, legacy markers, component attributes, and caller metadata rather
// than flushing a still-open pipe.

// TestInit_DualOutput initializes JSON logging with standard streams and a
// temporary rotating file. It requires info on stdout, warning on stderr, and
// both messages in the file after closing captured pipe writers.
func TestInit_DualOutput(t *testing.T) {
	// Use best-effort directory cleanup because lumberjack may retain an open handle on Windows.
	tmpDir, err := os.MkdirTemp("", "logger-test-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(tmpDir)
	})
	logPath := filepath.Join(tmpDir, "logs", "tokenrouter.log")

	// This serial fixture owns the process stream replacement until cleanup
	// restores stdout and stderr. Its logger core retains the pipe writer
	// selected during Init.
	origStdout := os.Stdout
	origStderr := os.Stderr
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	os.Stdout = stdoutW
	os.Stderr = stderrW
	t.Cleanup(func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
		_ = stdoutR.Close()
		_ = stderrR.Close()
		_ = stdoutW.Close()
		_ = stderrW.Close()
	})

	err = Init(InitOptions{
		Level:       "debug",
		Format:      "json",
		ServiceName: "tokenrouter",
		Environment: "test",
		Output: OutputOptions{
			ToStdout: true,
			ToFile:   true,
			FilePath: logPath,
		},
		Rotation: RotationOptions{
			MaxSizeMB:  10,
			MaxBackups: 2,
			MaxAgeDays: 1,
		},
		Sampling: SamplingOptions{Enabled: false},
	})
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	L().Info("dual-output-info")
	L().Warn("dual-output-warn")

	// Close both pipe writers so captured reads can reach EOF. The logger keeps
	// the handles selected during Init, and Windows Sync can wait inside
	// FlushFileBuffers until a pipe consumer reads its buffered bytes.

	_ = stdoutW.Close()
	_ = stderrW.Close()
	stdoutBytes, _ := io.ReadAll(stdoutR)
	stderrBytes, _ := io.ReadAll(stderrR)
	stdoutText := string(stdoutBytes)
	stderrText := string(stderrBytes)

	if !strings.Contains(stdoutText, "dual-output-info") {
		t.Fatalf("stdout missing info log: %s", stdoutText)
	}
	if !strings.Contains(stderrText, "dual-output-warn") {
		t.Fatalf("stderr missing warn log: %s", stderrText)
	}

	fileBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	fileText := string(fileBytes)
	if !strings.Contains(fileText, "dual-output-info") || !strings.Contains(fileText, "dual-output-warn") {
		t.Fatalf("file missing logs: %s", fileText)
	}
}

// TestInit_FileOutputFailureDowngrade uses an impossible directory beneath
// os.DevNull for file output. It requires successful logger initialization
// and a direct stderr setup warning, protecting availability when the file
// core cannot be built.
func TestInit_FileOutputFailureDowngrade(t *testing.T) {
	// This serial fixture owns the process stream replacement until cleanup
	// restores stdout and stderr. Its logger core retains the pipe writer
	// selected during Init.
	origStdout := os.Stdout
	origStderr := os.Stderr
	_, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	os.Stdout = stdoutW
	os.Stderr = stderrW
	t.Cleanup(func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
		_ = stdoutW.Close()
		_ = stderrR.Close()
		_ = stderrW.Close()
	})

	err = Init(InitOptions{
		Level:  "info",
		Format: "json",
		Output: OutputOptions{
			ToStdout: true,
			ToFile:   true,
			FilePath: filepath.Join(os.DevNull, "logs", "tokenrouter.log"),
		},
		Rotation: RotationOptions{
			MaxSizeMB:  10,
			MaxBackups: 1,
			MaxAgeDays: 1,
		},
	})
	if err != nil {
		t.Fatalf("Init() should downgrade instead of failing, got: %v", err)
	}

	// Finish the captured stderr stream before reading its setup warning, so
	// the assertion can reach EOF even while the process logger still retains
	// its selected output handle.
	_ = stderrW.Close()
	stderrBytes, _ := io.ReadAll(stderrR)
	if !strings.Contains(string(stderrBytes), "Failed to initialize file logging") {
		t.Fatalf("stderr should contain fallback warning, got: %s", string(stderrBytes))
	}
}

// TestInit_CallerShouldPointToCallsite writes one JSON entry with caller
// metadata enabled. It requires the logged caller to reference this test file
// after captured stdout is closed and decoded.
func TestInit_CallerShouldPointToCallsite(t *testing.T) {
	// This serial fixture owns the process stream replacement until cleanup
	// restores stdout and stderr. Its logger core retains the pipe writer
	// selected during Init.
	origStdout := os.Stdout
	origStderr := os.Stderr
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	_, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	os.Stdout = stdoutW
	os.Stderr = stderrW
	t.Cleanup(func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = stderrW.Close()
	})

	if err := Init(InitOptions{
		Level:       "info",
		Format:      "json",
		ServiceName: "tokenrouter",
		Environment: "test",
		Caller:      true,
		Output: OutputOptions{
			ToStdout: true,
			ToFile:   false,
		},
		Sampling: SamplingOptions{Enabled: false},
	}); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	L().Info("caller-check")
	// Close the captured writer so its reader can reach EOF. Windows Sync can
	// wait inside FlushFileBuffers for a pipe consumer, so the assertions inspect
	// completed writes after closing the captured handle.
	os.Stdout = origStdout
	os.Stderr = origStderr
	_ = stdoutW.Close()
	logBytes, _ := io.ReadAll(stdoutR)

	var line string
	for item := range strings.SplitSeq(string(logBytes), "\n") {
		if strings.Contains(item, "caller-check") {
			line = item
			break
		}
	}
	if line == "" {
		t.Fatalf("log output missing caller-check: %s", string(logBytes))
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		t.Fatalf("parse log json failed: %v, line=%s", err, line)
	}
	caller, _ := payload["caller"].(string)
	if !strings.Contains(caller, "logger_test.go:") {
		t.Fatalf("caller should point to this test file, got: %s", caller)
	}
}

// TestLegacyPrintfCaller writes a compatibility message to a temporary JSON
// log with caller capture enabled. It requires the stored caller text to
// identify this test file after a synchronization attempt.
func TestLegacyPrintfCaller(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caller.json")
	err := Init(InitOptions{
		Level:  "info",
		Format: "json",
		Caller: true,
		Output: OutputOptions{ToFile: true, FilePath: path},
	})
	if err != nil {
		t.Fatal(err)
	}
	LegacyPrintf("compatibility", "caller marker")
	Sync()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "logger_test.go") {
		t.Fatalf("caller must point at the consumer: %s", content)
	}
}

// TestInferStdLogLevel supplies ordinary, explicitly leveled, failure, and
// retry messages to severity inference. Explicit levels must override failure
// words in English diagnostic messages, while unmarked messages retain the
// keyword-based selection used by legacy callers.
func TestInferStdLogLevel(t *testing.T) {
	cases := []struct {
		msg  string // supplied legacy message text
		want Level  // expected inferred zap severity
	}{
		{msg: "Warning: queue full", want: LevelWarn},
		{msg: "Forward request failed: timeout", want: LevelError},
		{msg: "[ERROR] upstream unavailable", want: LevelError},
		{msg: "[OpenAI WS Mode] reconnect_retry provider_id=22 retry=1 max_retries=5", want: LevelInfo},
		{msg: "service started", want: LevelInfo},
		{msg: "debug: cache miss", want: LevelDebug},
		{msg: "[INFO] [PreAggregation] Failed to read runtime configuration, using deployment defaults: context deadline exceeded", want: LevelInfo},
		{msg: "[info] refresh failed: upstream error", want: LevelInfo},
		{msg: "info: operation failed with a warning", want: LevelInfo},
		{msg: "[WARN] refresh failed: upstream error", want: LevelWarn},
		{msg: "[DEBUG] refresh failed: upstream error", want: LevelDebug},
	}

	for _, tc := range cases {
		got := inferStdLogLevel(tc.msg)
		if got != tc.want {
			t.Fatalf("inferStdLogLevel(%q)=%v want=%v", tc.msg, got, tc.want)
		}
	}
}

// TestNormalizeStdLogMessage supplies multiline legacy text with repeated
// surrounding and internal whitespace. It requires one trimmed line with
// single separators, protecting the message spelling used by compatibility
// logging.
func TestNormalizeStdLogMessage(t *testing.T) {
	raw := "  [TokenRefresh]  cycle complete \n total=1   failed=0 \n"
	got := normalizeStdLogMessage(raw)
	want := "[TokenRefresh] cycle complete total=1 failed=0"
	if got != want {
		t.Fatalf("normalizeStdLogMessage()=%q want=%q", got, want)
	}
}

// TestStdLogBridgeRoutesLevels sends ordinary, warning, and failed
// standard-log messages after Init. It requires severity-specific captured
// streams and the legacy_stdlog marker on warning/error output after pipe
// writers close. An explicit info prefix keeps a translated failure and its
// error detail on stdout.
func TestStdLogBridgeRoutesLevels(t *testing.T) {
	// This serial fixture owns the process stream replacement until cleanup
	// restores stdout and stderr. Its logger core retains the pipe writer
	// selected during Init.
	origStdout := os.Stdout
	origStderr := os.Stderr
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	os.Stdout = stdoutW
	os.Stderr = stderrW
	t.Cleanup(func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = stderrR.Close()
		_ = stderrW.Close()
	})

	if err := Init(InitOptions{
		Level:       "debug",
		Format:      "json",
		ServiceName: "tokenrouter",
		Environment: "test",
		Output: OutputOptions{
			ToStdout: true,
			ToFile:   false,
		},
		Sampling: SamplingOptions{Enabled: false},
	}); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	log.Printf("service started")
	log.Printf("Warning: queue full")
	log.Printf("Forward request failed: timeout")
	log.Printf("[INFO] [PreAggregation] Failed to read runtime configuration: %s", "upstream error")
	// Close both pipe writers so captured reads can reach EOF. The logger keeps
	// the handles selected during Init, and Windows Sync can wait inside
	// FlushFileBuffers until a pipe consumer reads its buffered bytes.

	_ = stdoutW.Close()
	_ = stderrW.Close()
	stdoutBytes, _ := io.ReadAll(stdoutR)
	stderrBytes, _ := io.ReadAll(stderrR)
	stdoutText := string(stdoutBytes)
	stderrText := string(stderrBytes)

	if !strings.Contains(stdoutText, "service started") {
		t.Fatalf("stdout missing info log: %s", stdoutText)
	}
	if !strings.Contains(stdoutText, "[INFO] [PreAggregation] Failed to read runtime configuration: upstream error") || strings.Contains(stderrText, "[PreAggregation]") {
		t.Fatalf("explicit info log routed incorrectly: stdout=%s stderr=%s", stdoutText, stderrText)
	}
	if !strings.Contains(stderrText, "Warning: queue full") {
		t.Fatalf("stderr missing warn log: %s", stderrText)
	}
	if !strings.Contains(stderrText, "Forward request failed: timeout") {
		t.Fatalf("stderr missing error log: %s", stderrText)
	}
	if !strings.Contains(stderrText, "\"legacy_stdlog\":true") {
		t.Fatalf("stderr missing legacy_stdlog marker: %s", stderrText)
	}
}

// TestLegacyPrintfRoutesLevels sends ordinary, warning, and failed printf
// messages through one component label. It requires severity routing,
// legacy_printf markers, and that component attribute in captured
// warning/error output. An explicit info prefix keeps translated cache
// failures on stdout even when their formatted detail contains error keywords.
func TestLegacyPrintfRoutesLevels(t *testing.T) {
	// This serial fixture owns the process stream replacement until cleanup
	// restores stdout and stderr. Its logger core retains the pipe writer
	// selected during Init.
	origStdout := os.Stdout
	origStderr := os.Stderr
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	os.Stdout = stdoutW
	os.Stderr = stderrW
	t.Cleanup(func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = stderrR.Close()
		_ = stderrW.Close()
	})

	if err := Init(InitOptions{
		Level:       "debug",
		Format:      "json",
		ServiceName: "tokenrouter",
		Environment: "test",
		Output: OutputOptions{
			ToStdout: true,
			ToFile:   false,
		},
		Sampling: SamplingOptions{Enabled: false},
	}); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	LegacyPrintf("service.test", "request started")
	LegacyPrintf("service.test", "Warning: queue full")
	LegacyPrintf("service.test", "forward failed: timeout")
	LegacyPrintf("service.test", "[INFO] [Dashboard] Failed to read dashboard cache: %s", "upstream error")
	// Close both pipe writers so captured reads can reach EOF. The logger keeps
	// the handles selected during Init, and Windows Sync can wait inside
	// FlushFileBuffers until a pipe consumer reads its buffered bytes.

	_ = stdoutW.Close()
	_ = stderrW.Close()
	stdoutBytes, _ := io.ReadAll(stdoutR)
	stderrBytes, _ := io.ReadAll(stderrR)
	stdoutText := string(stdoutBytes)
	stderrText := string(stderrBytes)

	if !strings.Contains(stdoutText, "request started") {
		t.Fatalf("stdout missing info log: %s", stdoutText)
	}
	if !strings.Contains(stdoutText, "[INFO] [Dashboard] Failed to read dashboard cache: upstream error") || strings.Contains(stderrText, "[Dashboard]") {
		t.Fatalf("explicit info log routed incorrectly: stdout=%s stderr=%s", stdoutText, stderrText)
	}
	if !strings.Contains(stderrText, "Warning: queue full") {
		t.Fatalf("stderr missing warn log: %s", stderrText)
	}
	if !strings.Contains(stderrText, "forward failed: timeout") {
		t.Fatalf("stderr missing error log: %s", stderrText)
	}
	if !strings.Contains(stderrText, "\"legacy_printf\":true") {
		t.Fatalf("stderr missing legacy_printf marker: %s", stderrText)
	}
	if !strings.Contains(stderrText, "\"component\":\"service.test\"") {
		t.Fatalf("stderr missing component field: %s", stderrText)
	}
}
