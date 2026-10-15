package redis

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
)

// provider_migration.go renames legacy provider runtime keys and records
// resumable migration progress.
// Provider runtime keys use a new entity name while existing Redis values and
// remaining TTLs need to survive upgrade. Each known prefix is scanned and
// matching :account: segments are renamed with an atomic collision check; a
// conflicting target stops startup while keeping both conflicting values.
//
// Progress is stored after the complete scanned batch has been handled, and
// earlier successful renames survive a later error. Restarting reuses that
// prefix's saved cursor, or skips a prefix marked done, before recording
// overall completion. Old instances must remain stopped throughout this work;
// the progress hash supplies recovery state and does not serialize multiple
// migration runners or support concurrent writers using the old names.

// providerMigrationState names the hash holding per-prefix scan progress and
// overall completion so a subsequent startup can continue remaining rename work.
const providerMigrationState = "migration:provider-names:v1"

// renameProviderKey atomically checks both key identities before RENAME, retaining
// value type and TTL while rejecting a target that could otherwise be overwritten.
var renameProviderKey = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return 0 end
if redis.call('EXISTS', KEYS[2]) ~= 0 then return -1 end
redis.call('RENAME', KEYS[1], KEYS[2])
return 1
`)

// MigrateProviderNames renames selected runtime key prefixes after old
// application instances have stopped. It reads saved progress and skips
// prefixes or the whole migration already marked done. Redis or
// progress-decoding errors stop processing.
//
// Each rename atomically preserves the value and TTL and rejects an existing
// target. A batch cursor is saved after all its keys have been handled, so
// completed earlier renames remain when a later collision or state write
// fails. Callers serialize migration runners and resolve collisions before
// retrying; versioned snapshots are rebuilt separately.
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
			// Scan in bounded requested batches so startup can persist each prefix
			// cursor between groups; Redis may return fewer or more keys than requested.
			keys, next, err := client.Scan(ctx, cursor, prefix+"*", 256).Result()
			if err != nil {
				return fmt.Errorf("scan provider state %s: %w", prefix, err)
			}
			// Rename this batch before advancing its cursor. Each Lua call checks a
			// target collision atomically; earlier renamed keys remain if a later key fails.
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
			// Persist the next cursor after all batch renames. A failure before
			// this write retains the prior cursor and the already completed key changes.
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
	// Mark the whole migration done after every prefix checkpoint succeeds, so
	// later startups can skip all completed rename work through one state lookup.
	return client.HSet(ctx, providerMigrationState, "done", "1").Err()
}
