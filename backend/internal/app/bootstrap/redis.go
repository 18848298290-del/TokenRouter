package bootstrap

import (
	"crypto/tls"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/config"
	redisinfra "github.com/TokenFlux/TokenRouter/internal/infra/redis"
)

// redis.go constructs the shared Redis client from deployment connection
// settings.
// Redis deployment settings determine credentials, pool sizing, and connection
// timeouts for the shared application client. Timeout values become durations
// in seconds, and enabling TLS sets the configured host as the server name
// with a TLS 1.2 minimum.

// InitRedis constructs a Redis client using cfg's connection settings and
// optional command timing. Supply a nonnil cfg, own client shutdown, and check
// connectivity through subsequent operations. Construction performs no readiness
// check, and command contexts are not strict socket-I/O budgets under the
// client policy established by buildRedisOptions.
//
// Ordinary commands retain go-redis's three retries after the initial attempt.
// A returned write error does not establish that Redis made no mutation, and
// one adapter invocation can involve multiple server executions when an eligible
// failure is retried. Callers account for that possibility in write recovery.
func InitRedis(cfg *config.Config) *redis.Client {
	return redisinfra.NewClient(buildRedisOptions(cfg), cfg.Server.EnableServerTiming)
}

// buildRedisOptions converts Redis configuration to client options.
// Timeout fields are interpreted as seconds. Enabling TLS supplies the
// configured host as ServerName and requires TLS 1.2 or later.
//
// ContextTimeoutEnabled retains its false zero value. go-redis then uses a
// background context for command socket reads and writes, applying its client
// and command timeout rules rather than shortening them to a caller deadline.
// Pool waits and retry delays still receive the command context, so their
// cancellation policy remains separate from the socket read/write budgets.
//
// MaxRetries is also omitted: go-redis resolves zero to three retries, for an
// initial attempt plus eligible retry attempts. EOF, unexpected EOF, and eligible
// timeouts can be retried. Script.Run uses ordinary EvalSha/Eval commands, so a
// script adapter invocation can repeat server execution after an uncertain reply;
// a write error alone cannot establish that its server-side mutation was absent.
func buildRedisOptions(cfg *config.Config) *redis.Options {
	opts := &redis.Options{
		Addr:     cfg.Redis.Address(),
		Username: cfg.Redis.Username,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
		// Deployment timeout units are seconds; Redis consumes time.Duration values.
		DialTimeout:  time.Duration(cfg.Redis.DialTimeoutSeconds) * time.Second,
		ReadTimeout:  time.Duration(cfg.Redis.ReadTimeoutSeconds) * time.Second,
		WriteTimeout: time.Duration(cfg.Redis.WriteTimeoutSeconds) * time.Second,
		PoolSize:     cfg.Redis.PoolSize,
		MinIdleConns: cfg.Redis.MinIdleConns,
	}

	if cfg.Redis.EnableTLS {
		// Reject Redis servers negotiating older TLS versions before commands and
		// authentication credentials are sent over the established connection.
		opts.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: cfg.Redis.Host,
		}
	}

	return opts
}
