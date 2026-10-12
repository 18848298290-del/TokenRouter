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

	// Close the pipe writers before reading; Sync can block in Windows FlushFileBuffers.

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

func TestInit_FileOutputFailureDowngrade(t *testing.T) {
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

	_ = stderrW.Close()
	stderrBytes, _ := io.ReadAll(stderrR)
	if !strings.Contains(string(stderrBytes), "日志文件输出初始化失败") {
		t.Fatalf("stderr should contain fallback warning, got: %s", string(stderrBytes))
	}
}

func TestInit_CallerShouldPointToCallsite(t *testing.T) {
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
	// Close the pipe writer before reading; Sync can block in Windows FlushFileBuffers.
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

// TestLegacyPrintfCaller checks that caller metadata identifies the logging call site.
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

func TestInferStdLogLevel(t *testing.T) {
	cases := []struct {
		msg  string
		want Level
	}{
		{msg: "Warning: queue full", want: LevelWarn},
		{msg: "Forward request failed: timeout", want: LevelError},
		{msg: "[ERROR] upstream unavailable", want: LevelError},
		{msg: "[OpenAI WS Mode] reconnect_retry provider_id=22 retry=1 max_retries=5", want: LevelInfo},
		{msg: "service started", want: LevelInfo},
		{msg: "debug: cache miss", want: LevelDebug},
	}

	for _, tc := range cases {
		got := inferStdLogLevel(tc.msg)
		if got != tc.want {
			t.Fatalf("inferStdLogLevel(%q)=%v want=%v", tc.msg, got, tc.want)
		}
	}
}

func TestNormalizeStdLogMessage(t *testing.T) {
	raw := "  [TokenRefresh]  cycle complete \n total=1   failed=0 \n"
	got := normalizeStdLogMessage(raw)
	want := "[TokenRefresh] cycle complete total=1 failed=0"
	if got != want {
		t.Fatalf("normalizeStdLogMessage()=%q want=%q", got, want)
	}
}

func TestStdLogBridgeRoutesLevels(t *testing.T) {
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
	// Close the pipe writers before reading; Sync can block in Windows FlushFileBuffers.

	_ = stdoutW.Close()
	_ = stderrW.Close()
	stdoutBytes, _ := io.ReadAll(stdoutR)
	stderrBytes, _ := io.ReadAll(stderrR)
	stdoutText := string(stdoutBytes)
	stderrText := string(stderrBytes)

	if !strings.Contains(stdoutText, "service started") {
		t.Fatalf("stdout missing info log: %s", stdoutText)
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

func TestLegacyPrintfRoutesLevels(t *testing.T) {
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
	// Close the pipe writers before reading; Sync can block in Windows FlushFileBuffers.

	_ = stdoutW.Close()
	_ = stderrW.Close()
	stdoutBytes, _ := io.ReadAll(stdoutR)
	stderrBytes, _ := io.ReadAll(stderrR)
	stdoutText := string(stdoutBytes)
	stderrText := string(stderrBytes)

	if !strings.Contains(stdoutText, "request started") {
		t.Fatalf("stdout missing info log: %s", stdoutText)
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
