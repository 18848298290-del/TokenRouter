package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
)

// Rejection access sampling shares a budget across callers and reports skipped
// events on a separate schedule. Concurrent admission, Logger integration, and
// controlled timestamps check the global limit and the summary's exemption from
// the Ops system-log sink.

// TestIngressRejectAccessSamplerConcurrentGlobalLimit releases many callers
// against the same sampler timestamp. Their admitted count stays within one
// shared budget after every goroutine finishes.
func TestIngressRejectAccessSamplerConcurrentGlobalLimit(t *testing.T) {
	sampler := newIngressRejectAccessSampler(10, time.Hour, time.Minute)
	now := time.Now()
	var allowed atomic.Int64
	var wg sync.WaitGroup
	// Use one timestamp for every goroutine so none can obtain a fresh window.
	// Wait for all decisions before asserting the shared admission count.
	for range 200 {
		wg.Go(func() {
			if ok, _ := sampler.allow(now); ok {
				allowed.Add(1)
			}
		})
	}
	wg.Wait()
	require.Equal(t, int64(10), allowed.Load())
}

// TestLoggerIngressRejectSamplingIsBoundedAndSummarySkipsOpsSink serves
// repeated marked rejections through Logger with a small shared budget. Access
// emission stays bounded and the skipped-event summary carries the Ops sink
// exclusion flag.
func TestLoggerIngressRejectSamplingIsBoundedAndSummarySkipsOpsSink(t *testing.T) {
	// Replace the process-wide sampler for this serial test and restore its
	// pointer at cleanup. The fixture must outlive every request made through this
	// router.
	original := globalIngressRejectAccessSampler
	globalIngressRejectAccessSampler = newIngressRejectAccessSampler(2, time.Hour, time.Hour)
	t.Cleanup(func() { globalIngressRejectAccessSampler = original })
	sink := initMiddlewareTestLogger(t)
	router := gin.New()
	router.Use(Logger())
	router.GET("/v1/messages", func(c *gin.Context) {
		MarkIngressRejected(c, IngressRejectInvalidAPIKey)
		c.Status(http.StatusUnauthorized)
	})
	for range 20 {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/messages", nil))
	}
	var accessEvents, summaries int
	for _, event := range sink.list() {
		switch event.Message {
		case "http request completed":
			accessEvents++
		case "ingress rejection access logs dropped":
			summaries++
			if skipped, _ := event.Fields[logging.OpsSystemLogSkipField].(bool); !skipped {
				t.Fatalf("dropped summary must skip ops system log sink")
			}
		}
	}
	require.Equal(t, 2, accessEvents)
	require.Equal(t, 1, summaries)
}

// TestIngressRejectAccessSamplerDroppedSummaryIsLowFrequency advances
// timestamps while the emission budget remains exhausted. Early rejected calls
// accumulate skips, and a later due summary reports the count since the
// previous publication.
func TestIngressRejectAccessSamplerDroppedSummaryIsLowFrequency(t *testing.T) {
	sampler := newIngressRejectAccessSampler(1, time.Hour, time.Second)
	now := time.Now()
	allowed, summary := sampler.allow(now)
	require.True(t, allowed)
	require.Zero(t, summary)

	allowed, summary = sampler.allow(now.Add(100 * time.Millisecond))
	require.False(t, allowed)
	require.Equal(t, uint64(1), summary)
	allowed, summary = sampler.allow(now.Add(200 * time.Millisecond))
	require.False(t, allowed)
	require.Zero(t, summary)
	allowed, summary = sampler.allow(now.Add(2 * time.Second))
	require.False(t, allowed)
	require.Equal(t, uint64(2), summary)
}
