package bootstrap

import (
	"crypto/tls"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/config"
	redisinfra "github.com/TokenFlux/TokenRouter/internal/infra/redis"
)

// InitRedis creates a Redis client with pool and timeout settings from cfg.
func InitRedis(cfg *config.Config) *redis.Client {
	return redisinfra.NewClient(buildRedisOptions(cfg), cfg.Server.EnableServerTiming)
}

// buildRedisOptions converts Redis configuration to client options.
// Configured timeout values are in seconds.
func buildRedisOptions(cfg *config.Config) *redis.Options {
	opts := &redis.Options{
		Addr:         cfg.Redis.Address(),
		Username:     cfg.Redis.Username,
		Password:     cfg.Redis.Password,
		DB:           cfg.Redis.DB,
		DialTimeout:  time.Duration(cfg.Redis.DialTimeoutSeconds) * time.Second,
		ReadTimeout:  time.Duration(cfg.Redis.ReadTimeoutSeconds) * time.Second,
		WriteTimeout: time.Duration(cfg.Redis.WriteTimeoutSeconds) * time.Second,
		PoolSize:     cfg.Redis.PoolSize,
		MinIdleConns: cfg.Redis.MinIdleConns,
	}

	if cfg.Redis.EnableTLS {
		opts.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: cfg.Redis.Host,
		}
	}

	return opts
}
