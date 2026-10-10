package setup

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// installMutex prevents concurrent installation attempts (TOCTOU protection).
var installMutex sync.Mutex

// RegisterRoutes registers setup wizard routes
// RestartRequester 由 CLI 入口注入，setup 不依赖完整应用图。
type RestartRequester interface{ RequestRestart() error }

// SetupStatus represents the current setup state.
type SetupStatus struct {
	NeedsSetup bool   `json:"needs_setup"`
	Step       string `json:"step"`
}

// TestDatabaseRequest represents database test request.
type TestDatabaseRequest struct {
	Host     string `json:"host" binding:"required"`
	Port     int    `json:"port" binding:"required"`
	User     string `json:"user" binding:"required"`
	Password string `json:"password"`
	DBName   string `json:"dbname" binding:"required"`
	SSLMode  string `json:"sslmode"`
}

// TestRedisRequest represents Redis test request.
type TestRedisRequest struct {
	Host      string `json:"host" binding:"required"`
	Port      int    `json:"port" binding:"required"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	DB        int    `json:"db"`
	EnableTLS bool   `json:"enable_tls"`
}

// InstallRequest represents installation request.
type InstallRequest struct {
	Database DatabaseConfig `json:"database" binding:"required"`
	Redis    RedisConfig    `json:"redis" binding:"required"`
	Admin    AdminConfig    `json:"admin" binding:"required"`
	Server   ServerConfig   `json:"server"`
}

func RegisterRoutes(r *gin.Engine, restarters ...RestartRequester) {
	var restarter RestartRequester
	if len(restarters) > 0 {
		restarter = restarters[0]
	}
	setup := r.Group("/setup")
	{
		// Status endpoint is always accessible (read-only)
		setup.GET("/status", getStatus)

		// All modification endpoints are protected by setupGuard
		protected := setup.Group("")
		protected.Use(setupGuard())
		{
			protected.POST("/test-db", testDatabase)
			protected.POST("/test-redis", testRedis)
			protected.POST("/install", func(c *gin.Context) { installWithRestart(c, restarter) })
		}
	}
}

// getStatus returns the current setup status.
func getStatus(c *gin.Context) {
	response.Success(c, SetupStatus{
		NeedsSetup: NeedsSetup(),
		Step:       "welcome",
	})
}

// setupGuard middleware ensures setup endpoints are only accessible during setup mode.
func setupGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !NeedsSetup() {
			response.Error(c, http.StatusForbidden, "Setup is not allowed: system is already installed")
			c.Abort()
			return
		}
		c.Next()
	}
}

// validatePassword checks password strength.
func validatePassword(password string) error {
	if len(password) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
	if len(password) > 128 {
		return fmt.Errorf("password must be at most 128 characters")
	}
	return nil
}

// testDatabase tests database connection.
func testDatabase(c *gin.Context) {
	var req TestDatabaseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "Invalid request: "+err.Error())
		return
	}

	// Security: Validate all inputs to prevent injection attacks
	if !validateHostname(req.Host) {
		response.Error(c, http.StatusBadRequest, "Invalid hostname format")
		return
	}
	if !validatePort(req.Port) {
		response.Error(c, http.StatusBadRequest, "Invalid port number")
		return
	}
	if !validateUsername(req.User) {
		response.Error(c, http.StatusBadRequest, "Invalid username format")
		return
	}
	if !validateDBName(req.DBName) {
		response.Error(c, http.StatusBadRequest, "Invalid database name format")
		return
	}

	if req.SSLMode == "" {
		req.SSLMode = "disable"
	}
	if !validateSSLMode(req.SSLMode) {
		response.Error(c, http.StatusBadRequest, "Invalid SSL mode")
		return
	}

	cfg := &DatabaseConfig{
		Host:     req.Host,
		Port:     req.Port,
		User:     req.User,
		Password: req.Password,
		DBName:   req.DBName,
		SSLMode:  req.SSLMode,
	}

	if err := TestDatabaseConnection(cfg); err != nil {
		response.Error(c, http.StatusBadRequest, "Connection failed: "+err.Error())
		return
	}

	response.Success(c, gin.H{"message": "Connection successful"})
}

// testRedis tests Redis connection.
func testRedis(c *gin.Context) {
	var req TestRedisRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "Invalid request: "+err.Error())
		return
	}

	// Security: Validate inputs
	if !validateHostname(req.Host) {
		response.Error(c, http.StatusBadRequest, "Invalid hostname format")
		return
	}
	if !validatePort(req.Port) {
		response.Error(c, http.StatusBadRequest, "Invalid port number")
		return
	}
	if req.DB < 0 || req.DB > 15 {
		response.Error(c, http.StatusBadRequest, "Invalid Redis database number (0-15)")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if len(req.Username) > 128 {
		response.Error(c, http.StatusBadRequest, "Invalid Redis username")
		return
	}

	cfg := &RedisConfig{
		Host:      req.Host,
		Port:      req.Port,
		Username:  req.Username,
		Password:  req.Password,
		DB:        req.DB,
		EnableTLS: req.EnableTLS,
	}

	if err := TestRedisConnection(cfg); err != nil {
		response.Error(c, http.StatusBadRequest, "Connection failed: "+err.Error())
		return
	}

	response.Success(c, gin.H{"message": "Connection successful"})
}

// installWithRestart 执行首次安装，并在成功后请求重启。
func installWithRestart(c *gin.Context, restarter RestartRequester) {
	// TOCTOU Protection: Acquire mutex to prevent concurrent installation
	installMutex.Lock()
	defer installMutex.Unlock()

	// Double-check after acquiring lock
	if !NeedsSetup() {
		response.Error(c, http.StatusForbidden, "Setup is not allowed: system is already installed")
		return
	}

	var req InstallRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "Invalid request: "+err.Error())
		return
	}

	req.Admin.Email = strings.TrimSpace(req.Admin.Email)
	req.Database.Host = strings.TrimSpace(req.Database.Host)
	req.Database.User = strings.TrimSpace(req.Database.User)
	req.Database.DBName = strings.TrimSpace(req.Database.DBName)
	req.Redis.Host = strings.TrimSpace(req.Redis.Host)
	req.Redis.Username = strings.TrimSpace(req.Redis.Username)

	// COMPREHENSIVE INPUT VALIDATION
	// Database validation
	if !validateHostname(req.Database.Host) {
		response.Error(c, http.StatusBadRequest, "Invalid database hostname")
		return
	}
	if !validatePort(req.Database.Port) {
		response.Error(c, http.StatusBadRequest, "Invalid database port")
		return
	}
	if !validateUsername(req.Database.User) {
		response.Error(c, http.StatusBadRequest, "Invalid database username")
		return
	}
	if !validateDBName(req.Database.DBName) {
		response.Error(c, http.StatusBadRequest, "Invalid database name")
		return
	}

	// Redis validation
	if !validateHostname(req.Redis.Host) {
		response.Error(c, http.StatusBadRequest, "Invalid Redis hostname")
		return
	}
	if !validatePort(req.Redis.Port) {
		response.Error(c, http.StatusBadRequest, "Invalid Redis port")
		return
	}
	if req.Redis.DB < 0 || req.Redis.DB > 15 {
		response.Error(c, http.StatusBadRequest, "Invalid Redis database number")
		return
	}
	if len(req.Redis.Username) > 128 {
		response.Error(c, http.StatusBadRequest, "Invalid Redis username")
		return
	}

	// Admin validation
	if !validateEmail(req.Admin.Email) {
		response.Error(c, http.StatusBadRequest, "Invalid admin email format")
		return
	}
	if err := validatePassword(req.Admin.Password); err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}

	// Server validation
	if req.Server.Port != 0 && !validatePort(req.Server.Port) {
		response.Error(c, http.StatusBadRequest, "Invalid server port")
		return
	}

	// SET DEFAULTS
	if req.Database.SSLMode == "" {
		req.Database.SSLMode = "disable"
	}
	if !validateSSLMode(req.Database.SSLMode) {
		response.Error(c, http.StatusBadRequest, "Invalid SSL mode")
		return
	}
	if req.Server.Host == "" {
		req.Server.Host = "0.0.0.0"
	}
	if req.Server.Port == 0 {
		req.Server.Port = 8080
	}
	if req.Server.Mode == "" {
		req.Server.Mode = "release"
	}
	// Validate server mode
	if req.Server.Mode != "release" && req.Server.Mode != "debug" {
		response.Error(c, http.StatusBadRequest, "Invalid server mode (must be 'release' or 'debug')")
		return
	}

	cfg := &SetupConfig{
		Database: req.Database,
		Redis:    req.Redis,
		Admin:    req.Admin,
		Server:   req.Server,
		JWT: JWTConfig{
			ExpireHour: 24,
		},
	}

	if err := Install(cfg); err != nil {
		response.Error(c, http.StatusInternalServerError, "Installation failed: "+err.Error())
		return
	}

	if restarter != nil {
		_ = restarter.RequestRestart()
	}

	response.Success(c, gin.H{
		"message": "Installation completed successfully. Service will restart automatically.",
		"restart": true,
	})
}
