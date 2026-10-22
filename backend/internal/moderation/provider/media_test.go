package provider

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/moderation/contract"
)

// TestContentModerationSnapshotStopsImmediatelyWhenContextCanceled supplies one
// hundred pending images with an already-canceled context. It checks preserved
// result count and a cancellation error status for every entry, without
// asserting elapsed time.
func TestContentModerationSnapshotStopsImmediatelyWhenContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel before dispatch to distinguish rejection of pending references from
	// interruption of an image download that has already started in a worker.
	cancel()
	media := make([]contract.ContentModerationMedia, 100)
	for index := range media {
		media[index] = contract.ContentModerationMedia{OriginalRef: "https://example.com/image.png", SnapshotStatus: "pending"}
	}

	result := SnapshotMedia(ctx, media)

	require.Len(t, result, len(media))
	for _, item := range result {
		require.Equal(t, "error", item.SnapshotStatus)
		require.Contains(t, item.SnapshotError, context.Canceled.Error())
	}
}

// TestContentModerationSnapshotTotalByteBudgetIsAtomic makes two serial
// reservations around the total limit. It checks that the rejected second claim
// leaves the counter unchanged; this case does not schedule competing CAS
// operations.
func TestContentModerationSnapshotTotalByteBudgetIsAtomic(t *testing.T) {
	var retained atomic.Int64

	require.True(t, reserveContentModerationSnapshotBytes(&retained, maxContentModerationSnapshotTotalBytes-1))
	require.False(t, reserveContentModerationSnapshotBytes(&retained, 2))
	require.Equal(t, int64(maxContentModerationSnapshotTotalBytes-1), retained.Load())
}

// TestContentModerationDataImageRejectsDecodedPayloadOverLimit supplies a
// base64 payload whose decoded size exceeds the individual image cap. It checks
// that decoding returns an error carrying the size-limit message.
func TestContentModerationDataImageRejectsDecodedPayloadOverLimit(t *testing.T) {
	payload := strings.Repeat("A", base64.StdEncoding.EncodedLen(maxContentModerationSnapshotBytes+1))

	_, _, err := decodeContentModerationDataImage("data:image/png;base64," + payload)

	require.Error(t, err)
	require.Contains(t, err.Error(), "20 MB")
}

// TestContentModerationSnapshotRejectsWrongMIMEAndPrivateRedirect calls MIME
// validation and the client redirect callback directly. It checks rejection of
// text/plain and a loopback redirect before any HTTP redirect exchange is made.
func TestContentModerationSnapshotRejectsWrongMIMEAndPrivateRedirect(t *testing.T) {
	_, err := normalizeContentModerationImageMIME("text/plain")
	require.Error(t, err)

	client := newContentModerationSnapshotClient()
	redirect := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/redirected.png", nil)
	require.Error(t, client.CheckRedirect(redirect, nil))
}

// TestContentModerationRemoteSnapshotRejectsPrivateAddress supplies a loopback
// HTTP image URL. It checks that fetching returns a private-address error
// during initial host validation before the download transport can connect.
func TestContentModerationRemoteSnapshotRejectsPrivateAddress(t *testing.T) {
	_, _, err := fetchContentModerationImage(context.Background(), "http://127.0.0.1/private.png")

	require.Error(t, err)
	require.Contains(t, err.Error(), "private or local image addresses")
}
