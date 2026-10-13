package antigravity

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Temporary response identifiers use fixed-length alphanumeric output, and
// message identifiers add an Anthropic prefix. Sample-based tests check format
// and observed duplicates, while direct atomic-counter tests exercise seed
// variation independently of the defensive entropy-error path.

// TestGenerateRandomID_Uniqueness samples one hundred generated temporary IDs
// and records observed collisions. Every sample must have twelve characters
// and differ from previously observed values in this run.
func TestGenerateRandomID_Uniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for i := range 100 {
		id := generateRandomID()
		require.Len(t, id, 12, "ID 长度应为 12")
		_, dup := seen[id]
		require.False(t, dup, "第 %d 次调用生成了重复 ID: %s", i, id)
		seen[id] = struct{}{}
	}
}

// TestGenerateAnthropicMsgID_FormatAndUniqueness samples message IDs with the
// Anthropic prefix and twenty-two alphanumeric characters. It rejects
// duplicates within the observed batch and checks each complete spelling.
func TestGenerateAnthropicMsgID_FormatAndUniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for i := range 100 {
		id := generateAnthropicMsgID()
		require.Regexp(t, `^msg_01[0-9A-Za-z]{22}$`, id)
		_, duplicate := seen[id]
		require.False(t, duplicate, "第 %d 次调用生成了重复 ID: %s", i, id)
		seen[id] = struct{}{}
	}
}

// TestFallbackCounter_Increments increments the defensive seed counter
// directly rather than forcing a random-reader error. Two consecutive
// operations must advance the observed value and return distinct counters.
func TestFallbackCounter_Increments(t *testing.T) {
	// Check the counter values used to vary time-based seeds.
	before := atomic.LoadUint64(&fallbackCounter)
	cnt1 := atomic.AddUint64(&fallbackCounter, 1)
	cnt2 := atomic.AddUint64(&fallbackCounter, 1)
	require.Equal(t, before+1, cnt1, "第一次递增应为 before+1")
	require.Equal(t, before+2, cnt2, "第二次递增应为 before+2")
	require.NotEqual(t, cnt1, cnt2, "连续两次递增的计数器值应不同")
}

// TestFallbackCounter_ConcurrentIncrements records direct atomic counter
// increments from fifty goroutines into separate result slots. The completed
// sample must contain distinct values after all writers have finished.
func TestFallbackCounter_ConcurrentIncrements(t *testing.T) {
	// Give each goroutine a separate result slot and join every writer before
	// duplicate checking, so map access remains in the collecting goroutine.
	const goroutines = 50
	results := make([]uint64, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			results[idx] = atomic.AddUint64(&fallbackCounter, 1)
		}(i)
	}
	wg.Wait()

	seen := make(map[uint64]bool, goroutines)
	for _, v := range results {
		assert.False(t, seen[v], "并发递增产生了重复值: %d", v)
		seen[v] = true
	}
}

// TestGenerateRandomID_Charset samples temporary IDs and checks every byte
// against the permitted alphabet. The observed output must contain only the
// configured lowercase, uppercase, and decimal characters.
func TestGenerateRandomID_Charset(t *testing.T) {
	const validChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	validSet := make(map[byte]struct{}, len(validChars))
	for i := range len(validChars) {
		validSet[validChars[i]] = struct{}{}
	}

	for range 50 {
		id := generateRandomID()
		for j := range len(id) {
			_, ok := validSet[id[j]]
			require.True(t, ok, "ID 包含非法字符: %c (ID=%s)", id[j], id)
		}
	}
}

// TestGenerateRandomID_Length repeats temporary-ID generation for one hundred
// samples. Each result must retain the twelve-character size used by response
// conversion callbacks across the observed batch.
func TestGenerateRandomID_Length(t *testing.T) {
	for range 100 {
		id := generateRandomID()
		assert.Len(t, id, 12, "每次生成的 ID 长度应为 12")
	}
}

// TestGenerateRandomID_ConcurrentUniqueness collects temporary IDs from one
// hundred goroutines and checks the completed batch. The observed results must
// be distinct after synchronization joins every writer.
func TestGenerateRandomID_ConcurrentUniqueness(t *testing.T) {
	const goroutines = 100
	results := make([]string, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)

	// Give each concurrent generator a distinct result slot, then wait for all
	// writes before inspecting observed duplicates in a single goroutine.
	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			results[idx] = generateRandomID()
		}(i)
	}
	wg.Wait()

	seen := make(map[string]bool, goroutines)
	for _, id := range results {
		assert.False(t, seen[id], "并发调用产生了重复 ID: %s", id)
		seen[id] = true
	}
}

// BenchmarkGenerateRandomID measures repeated temporary response-ID generation
// in the benchmark loop. Each iteration calls the generator so random-read and
// alphabet-mapping work contribute to the measurement.
func BenchmarkGenerateRandomID(b *testing.B) {
	for range b.N {
		_ = generateRandomID()
	}
}
