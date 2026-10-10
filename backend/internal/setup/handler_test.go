package setup

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestTestDatabaseValidationOrder(t *testing.T) {
	req := TestDatabaseRequest{Host: "invalid host", Port: -1, User: "invalid user", DBName: "1db", SSLMode: "Require"}
	steps := []struct {
		message string
		fix     func()
	}{
		{"Invalid hostname format", func() { req.Host = "localhost" }},
		{"Invalid port number", func() { req.Port = 5432 }},
		{"Invalid username format", func() { req.User = "postgres" }},
		{"Invalid database name format", func() { req.DBName = "tokenrouter" }},
		{"Invalid SSL mode", func() {}},
	}
	for _, step := range steps {
		t.Run(step.message, func(t *testing.T) {
			assertSetupValidationError(t, testDatabase, req, step.message)
		})
		step.fix()
	}
}

func TestTestRedisValidationOrder(t *testing.T) {
	req := TestRedisRequest{Host: "invalid host", Port: -1, DB: -1, Username: strings.Repeat("a", 129)}
	steps := []struct {
		message string
		fix     func()
	}{
		{"Invalid hostname format", func() { req.Host = "localhost" }},
		{"Invalid port number", func() { req.Port = 6379 }},
		{"Invalid Redis database number (0-15)", func() { req.DB = 0 }},
		{"Invalid Redis username", func() {}},
	}
	for _, step := range steps {
		t.Run(step.message, func(t *testing.T) {
			assertSetupValidationError(t, testRedis, req, step.message)
		})
		step.fix()
	}
}

func TestInstallWithRestartValidationOrder(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("SKIP_SETUP", "false")
	req := InstallRequest{
		Database: DatabaseConfig{Host: "invalid host", Port: -1, User: "invalid user", DBName: "1db", SSLMode: "Require"},
		Redis:    RedisConfig{Host: "invalid host", Port: -1, DB: -1, Username: strings.Repeat("a", 129)},
		Admin:    AdminConfig{Email: "invalid email", Password: "short"},
		Server:   ServerConfig{Port: -1, Mode: "invalid"},
	}
	steps := []struct {
		message string
		fix     func()
	}{
		{"Invalid database hostname", func() { req.Database.Host = " localhost " }},
		{"Invalid database port", func() { req.Database.Port = 5432 }},
		{"Invalid database username", func() { req.Database.User = " postgres " }},
		{"Invalid database name", func() { req.Database.DBName = " tokenrouter " }},
		{"Invalid Redis hostname", func() { req.Redis.Host = " localhost " }},
		{"Invalid Redis port", func() { req.Redis.Port = 6379 }},
		{"Invalid Redis database number", func() { req.Redis.DB = 0 }},
		{"Invalid Redis username", func() { req.Redis.Username = " redis " }},
		{"Invalid admin email format", func() { req.Admin.Email = " Admin <admin@example.com> " }},
		{"password must be at least 8 characters", func() { req.Admin.Password = strings.Repeat("a", 129) }},
		{"password must be at most 128 characters", func() { req.Admin.Password = "valid-password" }},
		{"Invalid server port", func() { req.Server.Port = 0 }},
		{"Invalid SSL mode", func() { req.Database.SSLMode = "" }},
		{"Invalid server mode (must be 'release' or 'debug')", func() {}},
	}
	for _, step := range steps {
		t.Run(step.message, func(t *testing.T) {
			assertSetupValidationError(t, func(c *gin.Context) { installWithRestart(c, nil) }, req, step.message)
		})
		step.fix()
	}
}

func assertSetupValidationError(t *testing.T, handler gin.HandlerFunc, req any, wantMessage string) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/setup/test", bytes.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler(ctx)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusBadRequest, recorder.Body)
	}
	var response struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Message != wantMessage {
		t.Errorf("message = %q, want %q", response.Message, wantMessage)
	}
}
