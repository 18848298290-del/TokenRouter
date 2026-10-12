//go:build integration

package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

type processOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

type testProcess struct {
	cmd    *exec.Cmd
	done   chan struct{}
	err    error
	output *processOutput
	stdin  io.WriteCloser
}

// Write locks the named buffer so io.Copy cannot bypass synchronization through a promoted method.
func (o *processOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buffer.Write(p)
}

func (o *processOutput) text() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buffer.String()
}

func startTestProcess(t *testing.T, binary, dir string, env []string, args ...string) *testProcess {
	t.Helper()
	p := &testProcess{cmd: exec.Command(binary, args...), done: make(chan struct{}), output: &processOutput{}}
	p.cmd.Dir = dir
	// The child receives test configuration and credentials instead of inheriting the parent environment.
	p.cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "TMPDIR=" + os.TempDir(), "TZ=UTC", "DATA_DIR=" + dir}, env...)
	p.cmd.Stdout = p.output
	p.cmd.Stderr = p.output
	var err error
	p.stdin, err = p.cmd.StdinPipe()
	require.NoError(t, err)
	require.NoError(t, p.cmd.Start())
	go func() { p.err = p.cmd.Wait(); close(p.done) }()
	t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			_ = p.cmd.Process.Kill()
			<-p.done
		}
		_ = p.stdin.Close()
	})
	return p
}

func (p *testProcess) wait(t *testing.T, timeout time.Duration) error {
	t.Helper()
	select {
	case <-p.done:
		return p.err
	case <-time.After(timeout):
		t.Fatalf("进程未在预算内退出：\n%s", p.output.text())
		return nil
	}
}

func (p *testProcess) prompt(t *testing.T, prompt, value string) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for !strings.Contains(p.output.text(), prompt) {
		select {
		case <-p.done:
			t.Fatalf("等待 %q 时进程退出：%v\n%s", prompt, p.err, p.output.text())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("没有出现提示 %q：\n%s", prompt, p.output.text())
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, err := io.WriteString(p.stdin, value+"\n")
	require.NoError(t, err)
}

func freeServerPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address, ok := ln.Addr().(*net.TCPAddr)
	require.True(t, ok)
	port := address.Port
	require.NoError(t, ln.Close())
	return port
}

func waitProcessHTTP(t *testing.T, p *testProcess, port int, path string) string {
	t.Helper()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-p.done:
			t.Fatalf("监听前进程退出：%v\n%s", p.err, p.output.text())
		default:
		}
		response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr == nil && response.StatusCode == http.StatusOK {
				return string(body)
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("HTTP 未就绪：\n%s", p.output.text())
	return ""
}

func TestProcessModes(t *testing.T) {
	backendRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	binary := filepath.Join(t.TempDir(), "server")
	// The build inherits toolchain settings; go.mod specifies the Go version.
	build := exec.Command("go", "build", "-ldflags=-X main.Version=test-contract -X main.Commit=test-head -X main.Date=test-date -X main.BuildType=test", "-o", binary, "./cmd/server")
	build.Dir = backendRoot
	buildOutput, err := build.CombinedOutput()
	require.NoError(t, err, string(buildOutput))
	t.Run("version", func(t *testing.T) {
		p := startTestProcess(t, binary, t.TempDir(), nil, "-version")
		require.NoError(t, p.wait(t, 30*time.Second))
		require.Contains(t, p.output.text(), "TokenRouter test-contract (commit: test-head, built: test-date)")
		require.NotContains(t, p.output.text(), "[Lifecycle] started")
	})
	fixture := newDatabaseFixture(t)
	ctx := context.Background()
	rdb, err := rediscontainer.Run(ctx, "redis:8.4-alpine")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rdb.Terminate(context.Background())) })
	redisHost, err := rdb.Host(ctx)
	require.NoError(t, err)
	redisPort, err := rdb.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)
	pricing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"test-model":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002}}`)
	}))
	t.Cleanup(pricing.Close)
	configFor := func(t *testing.T, mode, dbname string, port int) (string, []string) {
		t.Helper()
		dir := t.TempDir()
		priceFile := filepath.Join(dir, "prices.json")
		require.NoError(t, os.WriteFile(priceFile, []byte(`{"test-model":{"input_cost_per_token":0.000001}}`), 0o600))
		cfg := map[string]any{
			"run_mode": mode, "timezone": "UTC",
			"server":   map[string]any{"host": "127.0.0.1", "port": port, "mode": "release"},
			"database": map[string]any{"host": fixture.host, "port": fixture.port, "user": "postgres", "password": "postgres", "dbname": dbname, "sslmode": "disable", "max_open_conns": 16, "max_idle_conns": 2},
			"redis":    map[string]any{"host": redisHost, "port": redisPort.Int(), "pool_size": 16, "min_idle_conns": 0},
			"pricing":  map[string]any{"remote_url": "", "hash_url": "", "data_dir": dir, "fallback_file": priceFile},
			"log":      map[string]any{"level": "info", "output": map[string]any{"to_stdout": true, "to_file": false}},
		}
		data, err := yaml.Marshal(cfg)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o600))
		return dir, []string{"PGAPPNAME=test-" + mode}
	}
	// Legacy run-mode settings must leave administrator concurrency unchanged at startup.
	for _, mode := range []string{"standard", "simple"} {
		t.Run(mode+"-sigterm", func(t *testing.T) {
			administrators := make(map[int64]int)
			for _, concurrency := range []int{5, 12, 30} {
				row, createErr := fixture.client.User.Create().SetEmail(fmt.Sprintf("upgrade-%s-%d@example.test", mode, concurrency)).SetPasswordHash("hash").SetRole("admin").SetConcurrency(concurrency).Save(ctx)
				require.NoError(t, createErr)
				administrators[row.ID] = concurrency
				t.Cleanup(func() { require.NoError(t, fixture.client.User.DeleteOneID(row.ID).Exec(context.Background())) })
			}
			port := freeServerPort(t)
			dir, env := configFor(t, mode, "test_contracts", port)
			p := startTestProcess(t, binary, dir, env)
			waitProcessHTTP(t, p, port, "/health")
			for id, concurrency := range administrators {
				current, readErr := fixture.client.User.Get(ctx, id)
				require.NoError(t, readErr)
				require.Equal(t, concurrency, current.Concurrency)
			}
			require.NoError(t, p.cmd.Process.Signal(syscall.SIGTERM))
			require.NoError(t, p.wait(t, 40*time.Second), p.output.text())
			logs := p.output.text()
			// The shared model catalog starts once and stops before storage closes.
			require.Equal(t, 1, strings.Count(logs, "[Lifecycle] started ModelCatalogInitialization"))
			require.Equal(t, 1, strings.Count(logs, "[Lifecycle] started ModelCatalogService"))
			require.Equal(t, 1, strings.Count(logs, "[Lifecycle] stopped ModelCatalogService"))
			require.Less(t, strings.Index(logs, "started ModelCatalogInitialization"), strings.Index(logs, "started ModelCatalogService"))
			require.Less(t, strings.Index(logs, "stopped ModelCatalogService"), strings.Index(logs, "stopped Redis"))
			for _, name := range []string{"HTTPRequests", "DeferredService", "TimingWheelService", "UsageLogBatchers", "Redis", "Ent"} {
				require.Contains(t, logs, "[Lifecycle] stopped "+name)
			}
			// Task submissions and downloads finish before workers, recovery, cleanup, and storage stop.
			require.Equal(t, 1, strings.Count(logs, "[Lifecycle] stopped TaskRequestsAndDownloads"))
			require.Less(t, strings.Index(logs, "stopped HTTPRequests"), strings.Index(logs, "stopped TaskRequestsAndDownloads"))
			for _, name := range []string{"CreativeWorkerRuntime", "BatchImageWorkerRuntime", "BatchImageCleanupService"} {
				require.Equal(t, 1, strings.Count(logs, "[Lifecycle] started "+name), name)
				require.Equal(t, 1, strings.Count(logs, "[Lifecycle] stopped "+name), name)
				require.Less(t, strings.Index(logs, "stopped TaskRequestsAndDownloads"), strings.Index(logs, "stopped "+name), name)
				require.Less(t, strings.Index(logs, "stopped "+name), strings.Index(logs, "stopped Redis"), name)
			}
			// Billing components start once and drain before Redis closes.
			for _, name := range []string{"BillingCacheService", "SubscriptionExpiryService"} {
				require.Equal(t, 1, strings.Count(logs, "[Lifecycle] started "+name))
				require.Equal(t, 1, strings.Count(logs, "[Lifecycle] stopped "+name))
				require.Less(t, strings.Index(logs, "stopped "+name), strings.Index(logs, "stopped Redis"))
			}
			// Authentication resources stop after requests; delayed persistent outbox entries remain for the next startup.
			for _, name := range []string{"APIKeyService", "AuthCacheInvalidationWorker"} {
				require.Equal(t, 1, strings.Count(logs, "[Lifecycle] started "+name))
				require.Equal(t, 1, strings.Count(logs, "[Lifecycle] stopped "+name))
				require.Less(t, strings.Index(logs, "stopped "+name), strings.Index(logs, "stopped Redis"))
			}
			require.Less(t, strings.Index(logs, "stopped HTTPRequests"), strings.Index(logs, "stopped APIKeyService"))
			require.Less(t, strings.Index(logs, "stopped AuthCacheInvalidationWorker"), strings.Index(logs, "stopped APIKeyService"))
			require.Less(t, strings.Index(logs, "stopped TimingWheelService"), strings.Index(logs, "stopped Redis"))
			require.Less(t, strings.Index(logs, "stopped Redis"), strings.Index(logs, "stopped Ent"))
			// Periodic maintenance starts once; its producers stop before shared refresh and query dependencies.
			for _, name := range []string{"TokenRefreshService", "ProviderExpiryService", "ProxyExpiryService", "ScheduledTestRunnerService", "GroupAvailabilityProbeRunnerService", "CNProviderBalanceCheckService", "OllamaCloudUsageService", "DeferredService", "TLSFingerprintProfileService", "TLSFingerprintRouterService"} {
				require.Equal(t, 1, strings.Count(logs, "[Lifecycle] started "+name), name)
				require.Equal(t, 1, strings.Count(logs, "[Lifecycle] stopped "+name), name)
				require.Less(t, strings.Index(logs, "stopped "+name), strings.Index(logs, "stopped Redis"), name)
			}
			for _, name := range []string{"ProviderRefreshCoordinator", "ProviderOAuthUsage", "ProviderUpstreamUsage", "ProviderImportProbes", "ProviderPrivacy", "ProviderTier", "ProviderModelList", "GrokQuotaProbes", "TLSFingerprintCollectorService"} {
				require.Equal(t, 1, strings.Count(logs, "[Lifecycle] stopped "+name), name)
				require.Less(t, strings.Index(logs, "stopped "+name), strings.Index(logs, "stopped Redis"), name)
			}
			require.Less(t, strings.Index(logs, "stopped HTTPRequests"), strings.Index(logs, "stopped TokenRefreshService"))
			require.Less(t, strings.Index(logs, "stopped TokenRefreshService"), strings.Index(logs, "stopped ProviderRefreshCoordinator"))
			require.Less(t, strings.Index(logs, "stopped DeferredService"), strings.Index(logs, "stopped TimingWheelService"))

			// Snapshot, concurrency, and serial-queue services stop after requests and before storage.
			for _, name := range []string{"SchedulerSnapshotService", "ConcurrencyService", "UserMessageQueueService"} {
				require.Equal(t, 1, strings.Count(logs, "[Lifecycle] started "+name), name)
				require.Equal(t, 1, strings.Count(logs, "[Lifecycle] stopped "+name), name)
				require.Less(t, strings.Index(logs, "stopped HTTPRequests"), strings.Index(logs, "stopped "+name), name)
				require.Less(t, strings.Index(logs, "stopped "+name), strings.Index(logs, "stopped Redis"), name)
			}

			// Observation producers, aggregation, and write queues stop after requests and before storage.
			for _, name := range []string{"OpsMetricsCollector", "OpsAggregationService", "OpsAlertEvaluatorService", "OpsCleanupService", "OpsScheduledReportService", "OpsService", "OpsIngressRejectAggregator", "DashboardAggregationService", "UsageCleanupService", "AuditLogService", "OpsSystemLogSink"} {
				require.Equal(t, 1, strings.Count(logs, "[Lifecycle] started "+name), name)
				require.Equal(t, 1, strings.Count(logs, "[Lifecycle] stopped "+name), name)
				require.Less(t, strings.Index(logs, "stopped HTTPRequests"), strings.Index(logs, "stopped "+name), name)
				require.Less(t, strings.Index(logs, "stopped "+name), strings.Index(logs, "stopped Redis"), name)
			}
			for _, name := range []string{"OpsWSRuntime", "OpsErrorLogWorkers", "UsageLogBatchers"} {
				require.Equal(t, 1, strings.Count(logs, "[Lifecycle] stopped "+name), name)
				require.Less(t, strings.Index(logs, "stopped HTTPRequests"), strings.Index(logs, "stopped "+name), name)
				require.Less(t, strings.Index(logs, "stopped "+name), strings.Index(logs, "stopped Redis"), name)
			}
			require.Less(t, strings.Index(logs, "stopped UsageCleanupService"), strings.Index(logs, "stopped DashboardAggregationService"))
			require.Less(t, strings.Index(logs, "stopped OpsErrorLogWorkers"), strings.Index(logs, "stopped OpsSystemLogSink"))

			// Cancel maintenance admission before waiting for HTTP and tasks, then close storage.
			for _, name := range []string{"BackupAdmission", "SystemMaintenanceAdmission"} {
				require.Equal(t, 1, strings.Count(logs, "[Lifecycle] stopped "+name), name)
				require.Less(t, strings.Index(logs, "stopped "+name), strings.Index(logs, "stopped HTTPRequests"), name)
			}
			for _, name := range []string{"BackupService", "SystemMaintenanceOperations"} {
				require.Equal(t, 1, strings.Count(logs, "[Lifecycle] stopped "+name), name)
				require.Less(t, strings.Index(logs, "stopped HTTPRequests"), strings.Index(logs, "stopped "+name), name)
				require.Less(t, strings.Index(logs, "stopped "+name), strings.Index(logs, "stopped Ent"), name)
			}

			// Shared search, moderation, and notification services stop after requests and before storage.
			for _, name := range []string{"WebSearchRuntime", "ContentModerationService", "EmailQueueService"} {
				require.Equal(t, 1, strings.Count(logs, "[Lifecycle] started "+name), name)
				require.Equal(t, 1, strings.Count(logs, "[Lifecycle] stopped "+name), name)
				require.Less(t, strings.Index(logs, "stopped HTTPRequests"), strings.Index(logs, "stopped "+name), name)
				require.Less(t, strings.Index(logs, "stopped "+name), strings.Index(logs, "stopped Redis"), name)
				require.Less(t, strings.Index(logs, "stopped "+name), strings.Index(logs, "stopped Ent"), name)
			}
			require.Less(t, strings.Index(logs, "stopped ContentModerationService"), strings.Index(logs, "stopped EmailQueueService"))

			// Payment producers stop before notification tasks drain and shared storage closes.
			require.Equal(t, 1, strings.Count(logs, "[Lifecycle] started PaymentOrderExpiryService"))
			require.Equal(t, 1, strings.Count(logs, "[Lifecycle] stopped PaymentOrderExpiryService"))
			require.Contains(t, logs, "[Lifecycle] stopped ApplicationBackgroundTasks")
			for _, pair := range [][2]string{{"HTTPRequests", "PaymentOrderExpiryService"}, {"PaymentOrderExpiryService", "ApplicationBackgroundTasks"}, {"ApplicationBackgroundTasks", "EmailQueueService"}, {"EmailQueueService", "Redis"}, {"PaymentOrderExpiryService", "Ent"}} {
				require.Less(t, strings.Index(logs, "stopped "+pair[0]), strings.Index(logs, "stopped "+pair[1]), pair)
			}

			// Requests and attempts share admission shutdown; credentials and quota resources stop before storage.
			for _, name := range []string{"GatewayRequestsAndAttempts", "QoderRequestsAndAttempts", "QoderCredentialSessions", "OpenAIQuotaActions", "OpenAIQuotaService"} {
				require.Equal(t, 1, strings.Count(logs, "[Lifecycle] stopped "+name), name)
				require.Less(t, strings.Index(logs, "stopped "+name), strings.Index(logs, "stopped Redis"), name)
				require.Less(t, strings.Index(logs, "stopped "+name), strings.Index(logs, "stopped Ent"), name)
			}
			for _, name := range []string{"OAuthService", "OpenAIOAuthService", "GeminiOAuthService", "AntigravityOAuthService", "QoderOAuthService", "GrokOAuthService"} {
				require.Equal(t, 1, strings.Count(logs, "[Lifecycle] started "+name), name)
				require.Equal(t, 1, strings.Count(logs, "[Lifecycle] stopped "+name), name)
				require.Less(t, strings.Index(logs, "stopped GatewayRequestsAndAttempts"), strings.Index(logs, "stopped "+name), name)
				require.Less(t, strings.Index(logs, "stopped "+name), strings.Index(logs, "stopped Redis"), name)
			}
			// Request draining and operation cancellation share a phase because handlers can depend on cancellation to
			// return.
			for _, name := range []string{"HTTPRequests", "GatewayRequestsAndAttempts", "QoderRequestsAndAttempts"} {
				require.Equal(t, 1, strings.Count(logs, "[Lifecycle] stopped "+name), name)
				require.Less(t, strings.Index(logs, "stopped "+name), strings.Index(logs, "stopped UsageRecordWorkerPool"), name)
			}
			require.Less(t, strings.Index(logs, "stopped HTTPHijackedConnections"), strings.Index(logs, "stopped OpenAIQuotaActions"))
			require.Less(t, strings.Index(logs, "stopped OpenAIQuotaActions"), strings.Index(logs, "stopped HTTPRequests"))
			require.Less(t, strings.Index(logs, "stopped OpenAIQuotaActions"), strings.Index(logs, "stopped OpenAIQuotaService"))
			require.Less(t, strings.Index(logs, "stopped QoderRequestsAndAttempts"), strings.Index(logs, "stopped QoderCredentialSessions"))
			require.NotContains(t, logs, "[Lifecycle] started OpenAILiveObservers")
			require.NotContains(t, logs, "[Lifecycle] started TLSFingerprintCollectorService")
			require.Eventually(t, func() bool {
				var n int
				err := fixture.db.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE application_name=$1`, "test-"+mode).Scan(&n)
				return err == nil && n == 0
			}, 3*time.Second, 25*time.Millisecond)
		})
	}

	// The JWT tool initializes identity lookup and signing; its output must pass signature validation.
	t.Run("jwtgen-minimal", func(t *testing.T) {
		tool := filepath.Join(t.TempDir(), "jwtgen")
		build := exec.Command("go", "build", "-o", tool, "./cmd/jwtgen")
		build.Dir = backendRoot
		data, e := build.CombinedOutput()
		require.NoError(t, e, string(data))
		var id int64
		email := "test-jwtgen@example.com"
		require.NoError(t, fixture.db.QueryRow("INSERT INTO users(email,password_hash,role,status) VALUES($1,'fixture','admin','active') RETURNING id", email).Scan(&id))
		defer func() { _, e := fixture.db.Exec("DELETE FROM users WHERE id=$1", id); require.NoError(t, e) }()
		var secret string
		require.NoError(t, fixture.db.QueryRow("SELECT value FROM security_secrets WHERE key='jwt_secret'").Scan(&secret))
		for _, args := range [][]string{nil, {"-email", email}} {
			dir, env := configFor(t, "standard", "test_contracts", freeServerPort(t))
			p := startTestProcess(t, tool, dir, env, args...)
			require.NoError(t, p.wait(t, 30*time.Second))
			output := p.output.text()
			require.Contains(t, output, "ADMIN_EMAIL="+email)
			require.Contains(t, output, fmt.Sprintf("ADMIN_USER_ID=%d", id))
			require.NotContains(t, output, "[Lifecycle] started")
			token := ""
			for line := range strings.SplitSeq(output, "\n") {
				if after, ok := strings.CutPrefix(line, "JWT="); ok {
					token = after
				}
			}
			verifier := identity.NewSessionService(identity.SessionOptions{Secret: secret}, nil, nil, nil, nil)
			claims, e := verifier.ValidateToken(token)
			require.NoError(t, e)
			require.Equal(t, id, claims.UserID)
			require.Equal(t, "admin", claims.Role)
		}
	})

	t.Run("cleanup-minimal", func(t *testing.T) {
		tool := filepath.Join(t.TempDir(), "cleanup-ingress-reject-logs")
		build := exec.Command("go", "build", "-o", tool, "./cmd/cleanup-ingress-reject-logs")
		build.Dir = backendRoot
		output, e := build.CombinedOutput()
		require.NoError(t, e, string(output))
		var id int64
		require.NoError(t, fixture.db.QueryRow(`INSERT INTO ops_error_logs(error_phase,error_type,status_code,error_body,created_at) VALUES('auth','auth',401,'{"code":"API_KEY_REQUIRED"}',NOW()-INTERVAL '1 hour') RETURNING id`).Scan(&id))
		defer func() { _, e := fixture.db.Exec("DELETE FROM ops_error_logs WHERE id=$1", id); require.NoError(t, e) }()
		before := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		for _, execute := range []bool{false, true} {
			dir, env := configFor(t, "standard", "test_contracts", freeServerPort(t))
			args := []string{"--before", before}
			if execute {
				args = append(args, "--execute")
			}
			process := startTestProcess(t, tool, dir, env, args...)
			require.NoError(t, process.wait(t, 30*time.Second), process.output.text())
			text := process.output.text()
			require.NotContains(t, text, "[Lifecycle] started")
			require.Contains(t, text, "reason=missing_key")
			var count int
			require.NoError(t, fixture.db.QueryRow("SELECT COUNT(*) FROM ops_error_logs WHERE id=$1", id).Scan(&count))
			if execute {
				require.Contains(t, text, "mode=execute")
				require.Zero(t, count)
			} else {
				require.Contains(t, text, "mode=dry-run")
				require.Contains(t, text, "deleted=0")
				require.Equal(t, 1, count)
			}
		}
		dir, env := configFor(t, "standard", "test_contracts", freeServerPort(t))
		invalid := startTestProcess(t, tool, dir, env)
		require.Error(t, invalid.wait(t, 30*time.Second))
		require.Contains(t, invalid.output.text(), "--before is required")
		require.NotContains(t, invalid.output.text(), "[Lifecycle] started")
	})

	t.Run("bootstrap-failure-closes-connection", func(t *testing.T) {
		var original string
		require.NoError(t, fixture.db.QueryRow(`SELECT value FROM security_secrets WHERE key='jwt_secret'`).Scan(&original))
		_, err := fixture.db.Exec(`UPDATE security_secrets SET value='short' WHERE key='jwt_secret'`)
		require.NoError(t, err)
		defer func() {
			_, err := fixture.db.Exec(`UPDATE security_secrets SET value=$1 WHERE key='jwt_secret'`, original)
			require.NoError(t, err)
		}()
		dir, env := configFor(t, "standard", "test_contracts", freeServerPort(t))
		p := startTestProcess(t, binary, dir, env)
		require.Error(t, p.wait(t, 30*time.Second))
		require.Contains(t, p.output.text(), "must be at least 32 bytes")
		require.NotContains(t, p.output.text(), "[Lifecycle] started")
		require.Eventually(t, func() bool {
			var n int
			err := fixture.db.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE application_name='test-standard'`).Scan(&n)
			return err == nil && n == 0
		}, 3*time.Second, 25*time.Millisecond)
	})
	t.Run("listen-failure-cleans-resources", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer func() { _ = ln.Close() }()
		address, ok := ln.Addr().(*net.TCPAddr)
		require.True(t, ok)
		dir, env := configFor(t, "standard", "test_contracts", address.Port)
		p := startTestProcess(t, binary, dir, env)
		require.Error(t, p.wait(t, 60*time.Second))
		require.Contains(t, p.output.text(), "address already in use")
		require.Contains(t, p.output.text(), "[Lifecycle] stopped Redis")
		require.Contains(t, p.output.text(), "[Lifecycle] stopped Ent")
	})
	t.Run("setup-server-only", func(t *testing.T) {
		port := freeServerPort(t)
		p := startTestProcess(t, binary, t.TempDir(), []string{"SERVER_HOST=127.0.0.1", "SERVER_PORT=" + strconv.Itoa(port)})
		require.Contains(t, waitProcessHTTP(t, p, port, "/setup/status"), `"needs_setup":true`)
		require.NoError(t, p.cmd.Process.Signal(syscall.SIGTERM))
		require.NoError(t, p.wait(t, 10*time.Second))
		require.NotContains(t, p.output.text(), "[Lifecycle] started")
	})
	t.Run("cli-setup", func(t *testing.T) {
		_, err := fixture.db.Exec(`CREATE DATABASE ` + pq.QuoteIdentifier("test_cli"))
		require.NoError(t, err)
		dir := t.TempDir()
		p := startTestProcess(t, binary, dir, nil, "-setup")
		// Send each line after its prompt so other readers cannot buffer password input ahead of time.
		for _, step := range [][2]string{
			{"PostgreSQL Host", fixture.host},
			{"PostgreSQL Port", strconv.Itoa(fixture.port)},
			{"PostgreSQL User", "postgres"},
			{"PostgreSQL Password", "postgres"},
			{"Database Name", "test_cli"},
			{"SSL Mode", "disable"},
			{"Redis Host", redisHost},
			{"Redis Port", strconv.Itoa(redisPort.Int())},
			{"Redis Password", ""},
			{"Redis DB", "0"},
			{"Enable Redis TLS?", "n"},
			{"Admin Email", "test-cli@example.test"},
			{"Admin Password", "test-test-password"},
			{"Confirm Password", "test-test-password"},
			{"Server Port", strconv.Itoa(freeServerPort(t))},
			{"Proceed with installation?", "y"},
		} {
			p.prompt(t, step[0], step[1])
		}
		require.NoError(t, p.wait(t, 90*time.Second), p.output.text())
		require.Contains(t, p.output.text(), "Installation Complete!")
		info, err := os.Stat(filepath.Join(dir, "config.yaml"))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		_, err = os.Stat(filepath.Join(dir, ".installed"))
		require.NoError(t, err)
		require.NotContains(t, p.output.text(), "[Lifecycle] started")
	})
	t.Run("auto-setup", func(t *testing.T) {
		_, err := fixture.db.Exec(`CREATE DATABASE ` + pq.QuoteIdentifier("test_auto"))
		require.NoError(t, err)
		dir := t.TempDir()
		port := freeServerPort(t)
		p := startTestProcess(t, binary, dir, []string{
			"AUTO_SETUP=true", "DATABASE_HOST=" + fixture.host, "DATABASE_PORT=" + strconv.Itoa(fixture.port), "DATABASE_USER=postgres", "DATABASE_PASSWORD=postgres", "DATABASE_DBNAME=test_auto", "DATABASE_SSLMODE=disable",
			"REDIS_HOST=" + redisHost, "REDIS_PORT=" + strconv.Itoa(redisPort.Int()), "ADMIN_EMAIL=test-auto@example.test", "ADMIN_PASSWORD=test-test-password", "SERVER_HOST=127.0.0.1", "SERVER_PORT=" + strconv.Itoa(port),
			"PRICING_REMOTE_URL=" + pricing.URL, "PRICING_HASH_URL=" + pricing.URL, "PRICING_DATA_DIR=" + dir, "LOG_OUTPUT_TO_FILE=false",
		})
		waitProcessHTTP(t, p, port, "/health")
		require.NoError(t, p.cmd.Process.Signal(syscall.SIGTERM))
		require.NoError(t, p.wait(t, 40*time.Second), p.output.text())
		_, err = os.Stat(filepath.Join(dir, ".installed"))
		require.NoError(t, err)
		require.Contains(t, p.output.text(), "Auto setup mode enabled")
	})
}
