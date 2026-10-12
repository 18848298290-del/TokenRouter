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

type DriveStorageInfo = google.DriveStorageInfo

// DriveClient reads Google Drive storage quotas.
type DriveClient interface {
	GetStorageQuota(ctx context.Context, accessToken, proxyURL string) (*DriveStorageInfo, error)
}

type driveClient struct{}

// NewDriveClient creates a new Drive API client.
func NewDriveClient() DriveClient {
	return &driveClient{}
}

// GetStorageQuota fetches storage quota from Google Drive API.
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
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}

	// Retry transient failures up to three total attempts with exponential backoff and jitter.
	var resp *http.Response
	maxRetries := 3
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	for attempt := range maxRetries {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("request cancelled: %w", ctx.Err())
		}

		resp, err = client.Do(req)
		if err != nil {
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
		// Keep upstream response bodies out of returned errors.
		return nil, fmt.Errorf("drive API error: status %d", resp.StatusCode)
	}

	defer func() { _ = resp.Body.Close() }()

	var result struct {
		StorageQuota struct {
			Limit string `json:"limit"` // Decimal byte count encoded as a JSON string.
			Usage string `json:"usage"`
		} `json:"storageQuota"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Missing or invalid decimal counts remain zero.
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
