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

// task_storage.go configures Redis queues, transient creative storage, and
// batch-image download limits.
// Creative and batch-image queues keep task reservation and locking names
// beside their lease lifetimes. Assembly supplies those Redis options from
// startup configuration, while transient creative data and batch downloads
// receive their separate retention and concurrency settings through their
// owning adapters.

// provideCreativeQueue binds creative queue names and reservation/lock
// lifetimes to Redis. Nil cfg passes nil options for constructor defaults;
// supplied configuration forwards key prefixes and seconds values to the
// creative queue adapter.
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

// provideCreativeTransientStore constructs Redis transient payload/output
// storage with its configured lifetime. Nil cfg passes constructor defaults,
// while supplied TransientTTLSeconds remains the retention input used by the
// creative adapter.
func provideCreativeTransientStore(client *redis.Client, cfg *config.Config) creative.CreativeTransientStore {
	if cfg == nil {
		return creativeredis.NewCreativeTransientStore(client, nil)
	}
	return creativeredis.NewCreativeTransientStore(client, &creativeredis.TransientOptions{
		TransientTTLSeconds: cfg.Creative.TransientTTLSeconds,
	})
}

// provideBatchQueue constructs batch-image task queues with configured names
// and lease durations. Nil cfg passes constructor defaults; supplied inflight
// and job-lock seconds are converted to durations before the Redis queue
// normalizes its options.
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
		// Inflight reservations and job locks consume lease durations converted from seconds.
		InflightTTL: time.Duration(cfg.BatchImage.InflightTTLSeconds) * time.Second,
		LockTTL:     time.Duration(cfg.BatchImage.JobLockTTLSeconds) * time.Second,
	})
}

// provideBatchDownloadLimiter binds per-user download concurrency and
// maximum-duration seconds to Redis. Nil cfg selects constructor defaults,
// while configured values remain inputs to the batch download lease adapter.
func provideBatchDownloadLimiter(client *redis.Client, cfg *config.Config) batchimage.BatchImageDownloadLimiter {
	if cfg == nil {
		return batchredis.NewBatchImageDownloadLimiter(client, nil)
	}
	return batchredis.NewBatchImageDownloadLimiter(client, &batchredis.DownloadOptions{
		MaxDownloadConcurrencyPerUser: cfg.BatchImage.MaxDownloadConcurrencyPerUser,
		MaxDownloadDurationSeconds:    cfg.BatchImage.MaxDownloadDurationSeconds,
	})
}
