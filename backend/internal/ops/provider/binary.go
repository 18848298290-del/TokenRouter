package provider

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/ops"
)

const (
	allowedDownloadHost = "github.com"
	allowedAssetHost    = "objects.githubusercontent.com"
	maxDownloadSize     = 500 * 1024 * 1024
)

// BinaryDownload downloads selected release assets and checksums.
type BinaryDownload interface {
	DownloadFile(context.Context, string, string, int64) error
	FetchChecksumFile(context.Context, string) ([]byte, error)
}

// BinaryInstaller replaces the configured executable; tests supply a temporary path.
type BinaryInstaller struct {
	rename func(string, string) error

	githubClient BinaryDownload
	executable   func() (string, error)
}

func NewBinaryInstaller(client BinaryDownload, executable func() (string, error)) *BinaryInstaller {
	if executable == nil {
		executable = os.Executable
	}
	return &BinaryInstaller{githubClient: client, executable: executable, rename: os.Rename}
}

func (s *BinaryInstaller) Apply(ctx context.Context, releaseAssets []ops.Asset) error {
	// Find matching archive and checksum for current platform
	archiveName := s.getArchiveName()
	var downloadURL string
	var checksumURL string

	// Prefer tokenrouter archives; accept sub2api archives for historical releases.
	for _, prefix := range []string{"tokenrouter_", "sub2api_"} {
		for _, asset := range releaseAssets {
			if strings.HasPrefix(asset.Name, prefix) && strings.Contains(asset.Name, archiveName) && !strings.HasSuffix(asset.Name, ".txt") {
				downloadURL = asset.DownloadURL
				break
			}
		}
		if downloadURL != "" {
			break
		}
	}
	for _, asset := range releaseAssets {
		if asset.Name == "checksums.txt" {
			checksumURL = asset.DownloadURL
		}
	}

	if downloadURL == "" {
		return fmt.Errorf("no compatible release found for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	// SECURITY: Validate download URL is from trusted domain
	if err := validateDownloadURL(downloadURL); err != nil {
		return fmt.Errorf("invalid download URL: %w", err)
	}
	if checksumURL != "" {
		if err := validateDownloadURL(checksumURL); err != nil {
			return fmt.Errorf("invalid checksum URL: %w", err)
		}
	}

	// Get current executable path
	exePath, err := s.executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("failed to resolve symlinks: %w", err)
	}

	exeDir := filepath.Dir(exePath)

	// Stage beside the executable so each rename stays on the same filesystem.
	tempDir, err := os.MkdirTemp(exeDir, ".tokenrouter-update-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	// Download archive
	archivePath := filepath.Join(tempDir, filepath.Base(downloadURL))
	if err := s.downloadFile(ctx, downloadURL, archivePath); err != nil {
		return fmt.Errorf("download failed: %w", err)
	}

	// Verify checksum if available
	if checksumURL != "" {
		if err := s.verifyChecksum(ctx, archivePath, checksumURL); err != nil {
			return fmt.Errorf("checksum verification failed: %w", err)
		}
	}

	// Extract binary from archive
	newBinaryPath := filepath.Join(tempDir, "tokenrouter")
	if err := s.extractBinary(archivePath, newBinaryPath); err != nil {
		return fmt.Errorf("extraction failed: %w", err)
	}

	// Set executable permission before replacement
	if err := os.Chmod(newBinaryPath, 0o755); err != nil {
		return fmt.Errorf("chmod failed: %w", err)
	}

	// Replacement uses two renames, with a gap while the executable path is absent:
	// 1. Move the current executable to its backup path.
	// 2. Move the staged binary to the executable path.
	// Attempt to restore the backup if the second rename fails.
	backupPath := exePath + ".backup"

	if err := ctx.Err(); err != nil {
		return err
	}
	// Remove old backup if exists
	_ = os.Remove(backupPath)

	// Step 1: Move current binary to backup
	if err := s.rename(exePath, backupPath); err != nil {
		return fmt.Errorf("backup failed: %w", err)
	}

	// Step 2: Move new binary to target location (atomic, same filesystem)
	if err := s.rename(newBinaryPath, exePath); err != nil {
		// Restore backup on failure
		if restoreErr := s.rename(backupPath, exePath); restoreErr != nil {
			return fmt.Errorf("replace failed and restore failed: %w (restore error: %v)", err, restoreErr)
		}
		return fmt.Errorf("replace failed (restored backup): %w", err)
	}

	// Keep the backup for rollback.
	// The next update removes it before creating a replacement backup.
	return nil
}

// Rollback restores the previous version

func (s *BinaryInstaller) Rollback() error {
	exePath, err := s.executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("failed to resolve symlinks: %w", err)
	}

	backupFile := exePath + ".backup"
	if _, err := os.Stat(backupFile); os.IsNotExist(err) {
		return fmt.Errorf("no backup found")
	}

	// Replace current with backup
	if err := s.rename(backupFile, exePath); err != nil {
		return fmt.Errorf("rollback failed: %w", err)
	}

	return nil
}

func (s *BinaryInstaller) downloadFile(ctx context.Context, downloadURL, dest string) error {
	return s.githubClient.DownloadFile(ctx, downloadURL, dest, maxDownloadSize)
}

func (s *BinaryInstaller) getArchiveName() string {
	osName := runtime.GOOS
	arch := runtime.GOARCH
	return fmt.Sprintf("%s_%s", osName, arch)
}

// validateDownloadURL requires HTTPS and an allowed GitHub host or subdomain.
// The check applies to this URL; redirects are handled by the download client.

func validateDownloadURL(rawURL string) error {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	// Must be HTTPS
	if parsedURL.Scheme != "https" {
		return fmt.Errorf("only HTTPS URLs are allowed")
	}

	// Check against allowed hosts
	host := parsedURL.Host
	// GitHub release URLs can be from github.com or objects.githubusercontent.com
	if host != allowedDownloadHost &&
		!strings.HasSuffix(host, "."+allowedDownloadHost) &&
		host != allowedAssetHost &&
		!strings.HasSuffix(host, "."+allowedAssetHost) {
		return fmt.Errorf("download from untrusted host: %s", host)
	}

	return nil
}

func (s *BinaryInstaller) verifyChecksum(ctx context.Context, filePath, checksumURL string) error {
	// Download checksums file
	checksumData, err := s.githubClient.FetchChecksumFile(ctx, checksumURL)
	if err != nil {
		return fmt.Errorf("failed to download checksums: %w", err)
	}

	// Calculate file hash
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	actualHash := hex.EncodeToString(h.Sum(nil))

	// Find expected hash in checksums file
	fileName := filepath.Base(filePath)
	scanner := bufio.NewScanner(strings.NewReader(string(checksumData)))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[1] == fileName {
			if parts[0] == actualHash {
				return nil
			}
			return fmt.Errorf("checksum mismatch: expected %s, got %s", parts[0], actualHash)
		}
	}

	return fmt.Errorf("checksum not found for %s", fileName)
}

func (s *BinaryInstaller) extractBinary(archivePath, destPath string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	var reader io.Reader = f

	// Handle gzip compression
	if strings.HasSuffix(archivePath, ".gz") || strings.HasSuffix(archivePath, ".tar.gz") || strings.HasSuffix(archivePath, ".tgz") {
		gzr, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer func() { _ = gzr.Close() }()
		reader = gzr
	}

	// Handle tar archive
	if strings.Contains(archivePath, ".tar") {
		tr := tar.NewReader(reader)
		legacyExtracted := false
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}

			// Extract matching basenames into the fixed destination path.
			// Reject entry names containing traversal components before extraction.
			baseName := filepath.Base(hdr.Name)

			// Check for path traversal attempts
			if strings.Contains(hdr.Name, "..") {
				return fmt.Errorf("path traversal attempt detected: %s", hdr.Name)
			}

			// Validate the entry is a regular file
			if hdr.Typeflag != tar.TypeReg {
				continue // Skip directories and special files
			}

			// Only extract the specific binary we need
			if baseName == "tokenrouter" || baseName == "tokenrouter.exe" || baseName == "sub2api" || baseName == "sub2api.exe" {
				// Limit the extracted binary to 500 MiB.
				const maxBinarySize = 500 * 1024 * 1024
				if hdr.Size > maxBinarySize {
					return fmt.Errorf("binary too large: %d bytes (max %d)", hdr.Size, maxBinarySize)
				}

				out, err := os.Create(destPath)
				if err != nil {
					return err
				}

				// Use LimitReader to prevent decompression bombs
				limited := io.LimitReader(tr, maxBinarySize)
				if _, err := io.Copy(out, limited); err != nil {
					_ = out.Close()
					return err
				}
				if err := out.Close(); err != nil {
					return err
				}
				if baseName == "tokenrouter" || baseName == "tokenrouter.exe" {
					return nil
				}
				// Keep the legacy entry as a fallback while searching for a tokenrouter entry.
				legacyExtracted = true
			}
		}
		if legacyExtracted {
			return nil
		}
		return fmt.Errorf("binary not found in archive")
	}

	// Direct copy for non-tar files (with size limit)
	const maxBinarySize = 500 * 1024 * 1024
	out, err := os.Create(destPath)
	if err != nil {
		return err
	}

	limited := io.LimitReader(reader, maxBinarySize)
	if _, err := io.Copy(out, limited); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
