package bootstrap

import (
	"crypto/tls"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/config"
	redisinfra "github.com/TokenFlux/TokenRouter/internal/infra/redis"
)

// Redis deployment settings determine credentials, pool sizing, and connection
// timeouts for the shared application client. Timeout values become durations
// in seconds, and enabling TLS sets the configured host as the server name
// with a TLS 1.2 minimum.

// InitRedis constructs a Redis client using cfg's connection settings and
// optional command timing. The caller owns client shutdown and checks
// connectivity through subsequent operations; construction itself performs no
// readiness check.
func InitRedis(cfg *config.Config) *redis.Client {
	return redisinfra.NewClient(buildRedisOptions(cfg), cfg.Server.EnableServerTiming)
}

// buildRedisOptions converts Redis configuration to client options.
// Timeout fields are interpreted as seconds. Enabling TLS supplies the
// configured host as ServerName and requires TLS 1.2 or later.
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
