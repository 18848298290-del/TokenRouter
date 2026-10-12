package redis

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
)

const providerMigrationState = "migration:provider-names:v1"

// renameProviderKey atomically renames a key with its type and TTL, rejecting target collisions.
var renameProviderKey = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return 0 end
if redis.call('EXISTS', KEYS[2]) ~= 0 then return -1 end
redis.call('RENAME', KEYS[1], KEYS[2])
return 1
`)

// MigrateProviderNames renames runtime keys after all old instances have stopped.
// Saved scan cursors allow retries. Sticky and RPM keys use IDs and need no rename;
// versioned snapshots are rebuilt separately.
// @project-doc docs/operations/upgrade_notes.md#provider_name_migration
func MigrateProviderNames(ctx context.Context, client *redis.Client) error {
	done, err := client.HGet(ctx, providerMigrationState, "done").Result()
	if err != nil && err != redis.Nil {
		return fmt.Errorf("read provider migration state: %w", err)
	}
	if done == "1" {
		return nil
	}
	prefixes := []string{
		"concurrency:account:", "concurrency:live:account:", "wait:account:",
		"session_limit:account:", "window_cost:account:", "temp_unsched:account:",
		"timeout_count:account:", "openai_403_count:account:", "internal500_count:account:",
		"oauth:token:", "oauth:refresh_lock:",
	}
	for _, prefix := range prefixes {
		progress, err := client.HGet(ctx, providerMigrationState, prefix).Result()
		if err != nil && err != redis.Nil {
			return err
		}
		if progress == "done" {
			continue
		}
		var cursor uint64
		if progress != "" {
			cursor, err = strconv.ParseUint(progress, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid provider migration cursor for %s: %w", prefix, err)
			}
		}
		for {
			keys, next, err := client.Scan(ctx, cursor, prefix+"*", 256).Result()
			if err != nil {
				return fmt.Errorf("scan provider state %s: %w", prefix, err)
			}
			for _, key := range keys {
				target := strings.ReplaceAll(key, ":account:", ":provider:")
				if target == key {
					continue
				}
				renamed, err := renameProviderKey.Run(ctx, client, []string{key, target}).Int()
				if err != nil {
					return fmt.Errorf("rename provider state: %w", err)
				}
				if renamed < 0 {
					return fmt.Errorf("provider state migration conflict in %s; resolve conflicting keys before retry", prefix)
				}
			}
			state := strconv.FormatUint(next, 10)
			if next == 0 {
				state = "done"
			}
			if err := client.HSet(ctx, providerMigrationState, prefix, state).Err(); err != nil {
				return err
			}
			if next == 0 {
				break
			}
			cursor = next
		}
	}
	return client.HSet(ctx, providerMigrationState, "done", "1").Err()
}
