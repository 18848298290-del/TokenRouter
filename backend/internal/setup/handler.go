package setup

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// handler.go serves setup probes and serializes HTTP installation before
// requesting restart.
// An installation request can wait while another HTTP request finishes setup
// and writes its configuration. installWithRestart holds a process-local mutex,
// then repeats NeedsSetup before binding and validating inputs, keeping later
// queued requests from entering another installation after those persisted
// files appear. The route guard also blocks storage probes once setup is
// closed.
//
// After Install completes, an optional restarter receives one restart request
// and its error is discarded. The success response always contains
// restart=true, including when no restarter was supplied, so that field
// announces the intended next client action rather than confirming a completed
// restart. Storage creation and configuration writes occur before that restart
// request and remain present if restart handling fails.

// installMutex serializes HTTP installation attempts in this process while
// their final guard, persistent setup work, and restart request run; other
// processes need their own coordination.
var installMutex sync.Mutex

// RestartRequester lets setup request a service restart after persistent
// installation succeeds.
type RestartRequester interface {
	// RequestRestart requests a service restart after installation has persisted.
	// The HTTP setup consumer discards its error and still sends the successful installation response.
	RequestRestart() error
}

// SetupStatus carries the current installation gate and wizard starting step
// for clients.
type SetupStatus struct {
	NeedsSetup bool   `json:"needs_setup"` // current installation gate result
	Step       string `json:"step"`        // wizard initial screen name
}

// TestDatabaseRequest holds HTTP probe inputs validated before PostgreSQL
// connection and creation checks.
type TestDatabaseRequest struct {
	Host     string `json:"host" binding:"required"`   // storage server hostname text
	Port     int    `json:"port" binding:"required"`   // storage TCP connection port
	User     string `json:"user" binding:"required"`   // PostgreSQL authentication username text
	Password string `json:"password"`                  // storage authentication secret text
	DBName   string `json:"dbname" binding:"required"` // target database identifier text
	// SSLMode is validated before connection probing. testDatabase uses disable
	// when this string is empty, matching the Web installation input default.
	SSLMode string `json:"sslmode"`
}

// TestRedisRequest holds HTTP probe inputs validated before a temporary Redis
// Ping check.
type TestRedisRequest struct {
	Host string `json:"host" binding:"required"` // storage server hostname text
	Port int    `json:"port" binding:"required"` // storage TCP connection port
	// Username is trimmed before its 128-byte check and Redis option creation.
	// The trimmed value is supplied to bootstrap for the temporary authenticated probe.
	Username  string `json:"username"`
	Password  string `json:"password"`   // storage authentication secret text
	DB        int    `json:"db"`         // selected Redis database number
	EnableTLS bool   `json:"enable_tls"` // Redis TLS probe switch
}

// InstallRequest supplies initial storage, administrator, and listener choices
// for the Web installer.
type InstallRequest struct {
	Database DatabaseConfig `json:"database" binding:"required"` // installation PostgreSQL connection settings
	Redis    RedisConfig    `json:"redis" binding:"required"`    // installation Redis connection settings
	Admin    AdminConfig    `json:"admin" binding:"required"`    // initial administrator credential values
	// Server supplies optional listener choices. installWithRestart fills an empty
	// Host, zero Port, and empty Mode with Web defaults before calling Install.
	Server ServerConfig `json:"server"`
}

// RegisterRoutes installs public setup status and guarded probe and
// installation endpoints on r. The first optional restarter is used; later
// entries are ignored, and status remains readable after setup closes.
func RegisterRoutes(r *gin.Engine, restarters ...RestartRequester) {
	var restarter RestartRequester
	if len(restarters) > 0 {
		restarter = restarters[0]
	}
	setup := r.Group("/setup")
	{
		// Status remains readable after setup completes.
		setup.GET("/status", getStatus)

		// Connection tests and installation are available only while setup is needed.
		protected := setup.Group("")
		protected.Use(setupGuard())
		{
			protected.POST("/test-db", testDatabase)
			protected.POST("/test-redis", testRedis)
			protected.POST("/install", func(c *gin.Context) { installWithRestart(c, restarter) })
		}
	}
}

// getStatus responds with the current NeedsSetup result and the welcome wizard
// step.
func getStatus(c *gin.Context) {
	response.Success(c, SetupStatus{
		NeedsSetup: NeedsSetup(),
		Step:       "welcome",
	})
}

// setupGuard returns middleware that rejects and aborts requests when
// NeedsSetup is false. Allowed requests continue to their endpoint, where
// installation repeats the gate after obtaining its serialization lock.
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

// validatePassword accepts initial administrator passwords from eight through
// 128 bytes. Error text uses character wording, but len measures UTF-8 bytes,
// and accepted text is retained unchanged for administrator creation.
func validatePassword(password string) error {
	// Use the same byte-count bounds as the interactive installer's password prompts.
	if len(password) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
	if len(password) > 128 {
		return fmt.Errorf("password must be at most 128 characters")
	}
	return nil
}

// testDatabase binds PostgreSQL inputs and checks host, port, username,
// database name, then SSL mode. Empty SSLMode selects disable; invalid fields
// and probe failures return HTTP 400, and a successful probe can have created
// the target database.
func testDatabase(c *gin.Context) {
	var req TestDatabaseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "Invalid request: "+err.Error())
		return
	}

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

// testRedis binds Redis inputs and checks host, port, database range, and
// trimmed username length. Probe failures return HTTP 400, while successful
// temporary Ping verification returns a success message; the bootstrap helper
// owns client cleanup.
func testRedis(c *gin.Context) {
	var req TestRedisRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "Invalid request: "+err.Error())
		return
	}

	if !validateHostname(req.Host) {
		response.Error(c, http.StatusBadRequest, "Invalid hostname format")
		return
	}
	if !validatePort(req.Port) {
		response.Error(c, http.StatusBadRequest, "Invalid port number")
		return
	}
	// The probe shares the CLI installer's supported Redis database choice range.
	if req.DB < 0 || req.DB > 15 {
		response.Error(c, http.StatusBadRequest, "Invalid Redis database number (0-15)")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	// Bound the trimmed Redis authentication username before creating probe options.
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

// installWithRestart validates Web setup input and runs Install while holding
// the process installation mutex. It repeats NeedsSetup after acquiring the
// lock because another installer can finish while this request waits.
//
// Selected identifiers and the email are trimmed before ordered validation;
// empty listener fields receive Web defaults, and the initial JWT expiry is
// twenty-four hours. Successful persistence precedes the optional restart
// request. RequestRestart errors are discarded and success always reports
// restart=true, so neither that field nor the response proves a restart
// occurred. Installation errors can retain already-completed database or file
// effects.
func installWithRestart(c *gin.Context, restarter RestartRequester) {
	installMutex.Lock()
	defer installMutex.Unlock()

	// A previous holder can persist installation evidence while this request waits.
	// Recheck the gate after serialization before admitting another input validation and Install sequence.
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

	if !validateHostname(req.Redis.Host) {
		response.Error(c, http.StatusBadRequest, "Invalid Redis hostname")
		return
	}
	if !validatePort(req.Redis.Port) {
		response.Error(c, http.StatusBadRequest, "Invalid Redis port")
		return
	}
	// Keep installation input within the database choices accepted by the probe route.
	if req.Redis.DB < 0 || req.Redis.DB > 15 {
		response.Error(c, http.StatusBadRequest, "Invalid Redis database number")
		return
	}
	// Use the probe endpoint's byte limit for persisted Redis username input.
	if len(req.Redis.Username) > 128 {
		response.Error(c, http.StatusBadRequest, "Invalid Redis username")
		return
	}

	if !validateEmail(req.Admin.Email) {
		response.Error(c, http.StatusBadRequest, "Invalid admin email format")
		return
	}
	if err := validatePassword(req.Admin.Password); err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}

	if req.Server.Port != 0 && !validatePort(req.Server.Port) {
		response.Error(c, http.StatusBadRequest, "Invalid server port")
		return
	}

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

	// Installation has already persisted before restart is requested. Discard its
	// scheduling error so restart handling does not replace the successful installation response.
	if restarter != nil {
		_ = restarter.RequestRestart()
	}

	response.Success(c, gin.H{
		"message": "Installation completed successfully. Service will restart automatically.",
		"restart": true,
	})
}
