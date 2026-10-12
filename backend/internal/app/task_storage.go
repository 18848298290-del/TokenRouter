package app

import (
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/batchimage"
	batchredis "github.com/TokenFlux/TokenRouter/internal/batchimage/rediscache"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/creative"
	creativeredis "github.com/TokenFlux/TokenRouter/internal/creative/rediscache"
)

// provideCreativeQueue binds queue keys and lease durations from creative configuration.
func provideCreativeQueue(client *redis.Client, cfg *config.Config) creative.CreativeRunQueue {
	if cfg == nil {
		return creativeredis.NewCreativeQueue(client, nil)
	}
	return creativeredis.NewCreativeQueue(client, &creativeredis.QueueOptions{
		InflightKeyPrefix:  cfg.Creative.InflightKeyPrefix,
		InflightTTLSeconds: cfg.Creative.InflightTTLSeconds,
		JobLockTTLSeconds:  cfg.Creative.JobLockTTLSeconds,
		LockKeyPrefix:      cfg.Creative.LockKeyPrefix,
		QueueActiveKey:     cfg.Creative.QueueActiveKey,
		QueueDelayedKey:    cfg.Creative.QueueDelayedKey,
		QueueReadyKey:      cfg.Creative.QueueReadyKey,
	})
}

// provideCreativeTransientStore binds the creative Redis transient store and retention settings.
func provideCreativeTransientStore(client *redis.Client, cfg *config.Config) creative.CreativeTransientStore {
	if cfg == nil {
		return creativeredis.NewCreativeTransientStore(client, nil)
	}
	return creativeredis.NewCreativeTransientStore(client, &creativeredis.TransientOptions{
		TransientTTLSeconds: cfg.Creative.TransientTTLSeconds,
	})
}

// provideBatchQueue converts configured seconds to durations, using queue defaults when configuration is absent.
func provideBatchQueue(client *redis.Client, cfg *config.Config) batchimage.BatchImageQueue {
	if cfg == nil {
		return batchredis.NewBatchImageQueue(client, nil)
	}
	return batchredis.NewBatchImageQueue(client, &batchredis.QueueOptions{
		ReadyKey:       cfg.BatchImage.QueueReadyKey,
		DelayedKey:     cfg.BatchImage.QueueDelayedKey,
		ActiveKey:      cfg.BatchImage.QueueActiveKey,
		InflightPrefix: cfg.BatchImage.InflightKeyPrefix,
		LockPrefix:     cfg.BatchImage.LockKeyPrefix,
		InflightTTL:    time.Duration(cfg.BatchImage.InflightTTLSeconds) * time.Second,
		LockTTL:        time.Duration(cfg.BatchImage.JobLockTTLSeconds) * time.Second,
	})
}

// provideBatchDownloadLimiter configures per-user concurrency and download lease duration.
func provideBatchDownloadLimiter(client *redis.Client, cfg *config.Config) batchimage.BatchImageDownloadLimiter {
	if cfg == nil {
		return batchredis.NewBatchImageDownloadLimiter(client, nil)
	}
	return batchredis.NewBatchImageDownloadLimiter(client, &batchredis.DownloadOptions{
		MaxDownloadConcurrencyPerUser: cfg.BatchImage.MaxDownloadConcurrencyPerUser,
		MaxDownloadDurationSeconds:    cfg.BatchImage.MaxDownloadDurationSeconds,
	})
}
