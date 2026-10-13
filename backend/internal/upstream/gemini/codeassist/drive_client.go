package codeassist

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/protocol/google"
)

// Drive quota observations report decimal byte counts as JSON strings. Missing
// or invalid counts remain zero, allowing authorization callers to classify
// storage plans from the decoded limit while the technical client reports
// transport and response errors.
//
// The read performs at most three requests, retrying transport errors and
// selected throttling/server statuses. Each retry waits with exponential delay
// plus jitter and can stop on context cancellation. For status retries the
// discarded response closes after the wait finishes, including cancellation;
// the final successful response closes after JSON decoding. A final failed
// status returns its number instead of response-body text.

// DriveStorageInfo stores decoded Drive storage capacity and usage in bytes.
type DriveStorageInfo = google.DriveStorageInfo

// DriveClient obtains one Drive quota observation through request-selected
// credentials and proxy.
type DriveClient interface {
	// GetStorageQuota reads Drive capacity and usage using the supplied bearer
	// token and proxy. It returns decoded byte counts or an exchange error, and
	// context cancellation can end retry waits.
	GetStorageQuota(ctx context.Context, accessToken, proxyURL string) (*DriveStorageInfo, error)
}

// driveClient implements Drive quota exchanges through the shared HTTP client
// factory.
type driveClient struct{}

// NewDriveClient creates a stateless implementation of the Drive quota-reading
// interface.
func NewDriveClient() DriveClient {
	return &driveClient{}
}

// GetStorageQuota reads Drive byte capacity and usage with a ten-second HTTP
// client timeout. Callers supply the bearer token and proxy; missing or
// invalid decimal count strings produce zero-valued fields.
//
// Transport failures and selected 429/500/502/503 statuses retry within three
// total requests using exponential waits plus subsecond jitter. Context
// cancellation interrupts those waits and is checked before each request.
// Discarded status responses close after waiting, while a final successful
// body closes after decoding. A failed final status returns its status number,
// and malformed successful JSON returns a decode error.
func (c *driveClient) GetStorageQuota(ctx context.Context, accessToken, proxyURL string) (*DriveStorageInfo, error) {
	const driveAPIURL = "https://www.googleapis.com/drive/v3/about?fields=storageQuota"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, driveAPIURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)

	client, err := httpclient.GetClient(httpclient.Options{
		ProxyURL: proxyURL,
		Timeout:  10 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client: %w", err)
	}

	sleepWithContext := func(d time.Duration) error {
		// Bind each backoff timer to caller cancellation so authorization can stop
		// waiting before another quota request is issued.
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}

	// Bound quota retries to three requests because authorization needs a result
	// or failure to continue project and tier discovery. Waits also observe
	// caller cancellation.
	var resp *http.Response
	maxRetries := 3
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	for attempt := range maxRetries {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("request cancelled: %w", ctx.Err())
		}

		resp, err = client.Do(req)
		if err != nil {
			// Retry a transport failure before the final attempt; waiting observes
			// cancellation before repeating the same quota request.
			if attempt < maxRetries-1 {
				backoff := time.Duration(1<<uint(attempt)) * time.Second
				jitter := time.Duration(rng.Intn(1000)) * time.Millisecond
				if err := sleepWithContext(backoff + jitter); err != nil {
					return nil, fmt.Errorf("request cancelled: %w", err)
				}
				continue
			}
			return nil, fmt.Errorf("network error after %d attempts: %w", maxRetries, err)
		}

		if resp.StatusCode == http.StatusOK {
			break
		}

		if (resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == http.StatusInternalServerError ||
			resp.StatusCode == http.StatusBadGateway ||
			resp.StatusCode == http.StatusServiceUnavailable) && attempt < maxRetries-1 {
			// Keep this failed response owned by the retry phase and close it after
			// waiting, including cancellation, before issuing the next quota request.
			if err := func() error {
				defer func() { _ = resp.Body.Close() }()
				backoff := time.Duration(1<<uint(attempt)) * time.Second
				jitter := time.Duration(rng.Intn(1000)) * time.Millisecond
				return sleepWithContext(backoff + jitter)
			}(); err != nil {
				return nil, fmt.Errorf("request cancelled: %w", err)
			}
			continue
		}

		break
	}

	if resp == nil {
		return nil, fmt.Errorf("request failed: no response received")
	}

	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		statusText := http.StatusText(resp.StatusCode)
		if statusText == "" {
			statusText = resp.Status
		}
		fmt.Printf("[DriveClient] Drive API error: status=%d, msg=%s\n", resp.StatusCode, statusText)
		// Return the failed status as the quota diagnostic because account callers
		// need failure classification and should not display the upstream response
		// body.
		return nil, fmt.Errorf("drive API error: status %d", resp.StatusCode)
	}

	defer func() { _ = resp.Body.Close() }()

	var result struct {
		// Limit and Usage are independent decimal byte strings from the server. Each
		// missing or unparseable value remains zero for the quota observation
		// returned to authorization.
		StorageQuota struct { // reported storage quota fields
			Limit string `json:"limit"` // decimal capacity byte count
			Usage string `json:"usage"` // decimal used byte count
		} `json:"storageQuota"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Parse the independent decimal byte fields after decoding. Missing values or
	// integer parse failures keep zero so account classification can handle
	// unavailable counts.
	var limit, usage int64
	if result.StorageQuota.Limit != "" {
		if val, err := strconv.ParseInt(result.StorageQuota.Limit, 10, 64); err == nil {
			limit = val
		}
	}
	if result.StorageQuota.Usage != "" {
		if val, err := strconv.ParseInt(result.StorageQuota.Usage, 10, 64); err == nil {
			usage = val
		}
	}

	return &DriveStorageInfo{
		Limit: limit,
		Usage: usage,
	}, nil
}
