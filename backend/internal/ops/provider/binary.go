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

// binary.go downloads release archives, selects executable entries, and
// replaces or rolls back the installed binary.
// Release archives use current tokenrouter names and historical sub2api names
// for the same runtime platform. Installation prefers a current-name archive,
// checks its initial download host, and verifies an archive hash only when
// checksums.txt is supplied. Extraction similarly keeps a historical binary
// while looking for a current-name entry.
//
// Preparation takes place beside the symlink-resolved executable. Replacement
// moves the current binary to an adjacent backup before installing the staged
// one, so the executable path is temporarily absent and a failed second move
// needs restoration. Cancellation is checked before that sequence and is no
// longer consulted during its filesystem moves. A successful update retains the
// preceding executable, and local rollback consumes that backup.

// Release downloads accept the listed HTTPS host spellings and pass one archive
// byte cap to the bound downloader.
const (
	allowedDownloadHost = "github.com"                    // primary release download host
	allowedAssetHost    = "objects.githubusercontent.com" // release asset storage host
	maxDownloadSize     = 500 * 1024 * 1024               // archive download byte cap
)

// BinaryDownload supplies context-aware asset and checksum reads for a selected
// release.
type BinaryDownload interface {
	// DownloadFile writes the supplied asset URL to the destination under context
	// and a byte limit. BinaryInstaller passes its archive cap; the downloader
	// defines network policy and incomplete-file handling on failure.
	DownloadFile(context.Context, string, string, int64) error
	// FetchChecksumFile reads the supplied checksum URL under context and returns
	// its bytes or an error. BinaryInstaller parses filename/hash pairs itself;
	// the downloader defines redirects and checksum-response size policy.
	FetchChecksumFile(context.Context, string) ([]byte, error)
}

// BinaryInstaller stages a release executable and replaces its resolved
// installed path.
type BinaryInstaller struct {
	// rename performs replacement and restoration moves. NewBinaryInstaller uses
	// os.Rename, while fixtures replace it to reproduce specific failure
	// positions.
	rename func(string, string) error

	// githubClient is the supplied downloader used for archives and optional
	// checksums. It remains borrowed; callers provide a usable implementation
	// before Apply.
	githubClient BinaryDownload
	// executable identifies the installed program path before symlink resolution.
	// A nil constructor argument selects os.Executable; Apply and Rollback resolve
	// that path separately on each call.
	executable func() (string, error)
}

// NewBinaryInstaller retains the downloader and executable-path callback,
// selecting os.Executable when the callback is nil. It performs no installation
// or download. Callers provide a nonnil downloader for Apply and serialize
// operations that target the same executable.
func NewBinaryInstaller(client BinaryDownload, executable func() (string, error)) *BinaryInstaller {
	if executable == nil {
		executable = os.Executable
	}
	return &BinaryInstaller{githubClient: client, executable: executable, rename: os.Rename}
}

// Apply installs the first platform-matching tokenrouter archive, considering
// sub2api names only when no current-name URL was selected. It uses the last
// checksums.txt URL when present and permits installation without that asset.
// Supply a nonnil context/downloader and serialize maintenance of the same
// executable and backup paths.
//
// The executable path is resolved through symlinks, and staging beside that
// resolved file keeps rename moves on its filesystem. Downloads, optional hash
// checking, extraction, and permission changes precede the cancellation check.
// Once replacement begins, context is no longer consulted: the old file moves
// to .backup, then the staged file moves into place.
//
// The pair of renames leaves a gap at the executable path. Failure of the
// second move attempts restoration; restoration failure leaves the caller to
// inspect the combined error and remaining backup. Successful installation
// retains the backup for Rollback, while a later update attempts to remove it
// before creating another.
func (s *BinaryInstaller) Apply(ctx context.Context, releaseAssets []ops.Asset) error {
	archiveName := s.getArchiveName()
	var downloadURL string
	var checksumURL string

	// Current product archive names take precedence regardless of asset order.
	// Only absence of a selected current-name URL opens the historical
	// sub2api-name search.
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

	// Validate both selected initial URLs before downloading or staging. The bound
	// downloader remains responsible for redirected destinations and
	// request-header policy.
	if err := validateDownloadURL(downloadURL); err != nil {
		return fmt.Errorf("invalid download URL: %w", err)
	}
	if checksumURL != "" {
		if err := validateDownloadURL(checksumURL); err != nil {
			return fmt.Errorf("invalid checksum URL: %w", err)
		}
	}

	exePath, err := s.executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("failed to resolve symlinks: %w", err)
	}

	exeDir := filepath.Dir(exePath)

	// Staging beside the symlink-resolved executable gives both replacement moves
	// that directory filesystem. Deferred removal is best effort and does not
	// change the install result.
	tempDir, err := os.MkdirTemp(exeDir, ".tokenrouter-update-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	// The local archive basename comes from the raw download URL text. That same
	// basename drives checksum matching, so query or fragment suffixes can affect
	// verification and format detection.
	archivePath := filepath.Join(tempDir, filepath.Base(downloadURL))
	if err := s.downloadFile(ctx, downloadURL, archivePath); err != nil {
		return fmt.Errorf("download failed: %w", err)
	}

	// A nonempty selected checksums.txt URL enables filename-specific hash
	// verification. Releases without this asset proceed directly to extraction and
	// carry no verified archive hash from this path.
	if checksumURL != "" {
		if err := s.verifyChecksum(ctx, archivePath, checksumURL); err != nil {
			return fmt.Errorf("checksum verification failed: %w", err)
		}
	}

	newBinaryPath := filepath.Join(tempDir, "tokenrouter")
	if err := s.extractBinary(archivePath, newBinaryPath); err != nil {
		return fmt.Errorf("extraction failed: %w", err)
	}

	if err := os.Chmod(newBinaryPath, 0o755); err != nil {
		return fmt.Errorf("chmod failed: %w", err)
	}

	// Move the installed file to .backup before moving the staged binary into
	// place. These separate moves temporarily remove the executable path; a
	// second-move failure requires compensation.
	backupPath := exePath + ".backup"

	// Honor cancellation after preparation and before any backup removal. After
	// this check, replacement and restoration use filesystem moves without another
	// context check.
	if err := ctx.Err(); err != nil {
		return err
	}
	// A later update replaces the prior rollback candidate. Ignore failure
	// removing that older backup and let the subsequent rename report any
	// inability to create its new backup.
	_ = os.Remove(backupPath)

	if err := s.rename(exePath, backupPath); err != nil {
		return fmt.Errorf("backup failed: %w", err)
	}

	if err := s.rename(newBinaryPath, exePath); err != nil {
		// The first move already removed the installed path. Attempt to restore it
		// from the new backup, retaining both failure descriptions if compensation
		// also fails.
		if restoreErr := s.rename(backupPath, exePath); restoreErr != nil {
			return fmt.Errorf("replace failed and restore failed: %w (restore error: %v)", err, restoreErr)
		}
		return fmt.Errorf("replace failed (restored backup): %w", err)
	}

	// The completed update leaves its immediately preceding executable in the
	// backup path. Rollback consumes it, and the next update attempts to replace
	// it with that update predecessor.
	return nil
}

// Rollback resolves the current executable and replaces it with its adjacent
// .backup through rename. Callers serialize it with installation and handle its
// error before restart. A missing backup fails; success consumes that backup,
// and no replacement backup of the current binary is retained. Resolving the
// current path happens first, so its absence fails even when a backup remains.
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
	// Only recognized backup absence stops at Stat. Other stat errors proceed to
	// rename, whose result decides whether the saved executable can replace the
	// current path.
	if _, err := os.Stat(backupFile); os.IsNotExist(err) {
		return fmt.Errorf("no backup found")
	}

	// A successful rename consumes the saved executable and replaces the installed
	// path. It does not save the displaced current binary as another rollback
	// candidate.
	if err := s.rename(backupFile, exePath); err != nil {
		return fmt.Errorf("rollback failed: %w", err)
	}

	return nil
}

// downloadFile delegates the asset URL and destination to the borrowed
// downloader with the 500 MiB archive cap. The downloader owns response
// validation and context handling; this wrapper returns its error directly.
func (s *BinaryInstaller) downloadFile(ctx context.Context, downloadURL, dest string) error {
	return s.githubClient.DownloadFile(ctx, downloadURL, dest, maxDownloadSize)
}

// getArchiveName returns the runtime operating-system and architecture fragment
// used for asset selection.
func (s *BinaryInstaller) getArchiveName() string {
	osName := runtime.GOOS
	arch := runtime.GOARCH
	return fmt.Sprintf("%s_%s", osName, arch)
}

// validateDownloadURL accepts an initial HTTPS URL whose Host exactly matches a
// listed GitHub host or its dotted subdomain suffix. Matching is case sensitive
// against the full Host, so an explicit port does not match the listed host
// spellings. Redirect validation belongs to the downloader, and this helper
// does not reject user information.
func validateDownloadURL(rawURL string) error {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	if parsedURL.Scheme != "https" {
		return fmt.Errorf("only HTTPS URLs are allowed")
	}

	host := parsedURL.Host
	// Compare the full Host spelling, including any explicit port, to these
	// allowlist forms. This initial-URL check does not decide where the HTTP
	// client may redirect.
	if host != allowedDownloadHost &&
		!strings.HasSuffix(host, "."+allowedDownloadHost) &&
		host != allowedAssetHost &&
		!strings.HasSuffix(host, "."+allowedAssetHost) {
		return fmt.Errorf("download from untrusted host: %s", host)
	}

	return nil
}

// verifyChecksum compares the entire staged archive SHA-256 with the first
// two-field checksum line whose filename exactly matches its local basename. It
// fetches the checksum text through the bound downloader, using the supplied
// context, and returns a download, file-read, missing-entry, or mismatch error.
//
// Whitespace splitting permits spaces or tabs between the hash and filename,
// but hash spelling and filename are compared exactly. A matching filename ends
// the search even when its hash differs; later duplicate entries cannot
// override it. Scanner termination is reported as a missing checksum when no
// matching entry was found, including scanner failures because its error is not
// inspected.
func (s *BinaryInstaller) verifyChecksum(ctx context.Context, filePath, checksumURL string) error {
	checksumData, err := s.githubClient.FetchChecksumFile(ctx, checksumURL)
	if err != nil {
		return fmt.Errorf("failed to download checksums: %w", err)
	}

	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	// The digest is determined by io.Copy before the read handle closes. Ignore
	// the close error when returning the hash comparison result.
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	actualHash := hex.EncodeToString(h.Sum(nil))

	// Match the local archive basename against exactly two whitespace-separated
	// fields. The first named entry determines success or mismatch, even if later
	// duplicate entries differ.
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

	// End-of-input and scanner failure both reach the missing-entry result because
	// scanner.Err is not inspected. An earlier matching line can return before
	// later checksum lines are read.
	return fmt.Errorf("checksum not found for %s", fileName)
}

// extractBinary writes the selected executable bytes to destPath, choosing
// decompression from archivePath spelling. Gzip suffixes select a gzip reader,
// while a .tar substring anywhere in the path selects tar traversal. A .tgz
// path alone therefore decodes gzip and copies its result directly.
//
// Tar traversal uses regular-file basenames and writes to the fixed
// destination, accepting tokenrouter or sub2api with optional .exe. Every entry
// name containing two adjacent dots is rejected before type selection. A legacy
// entry is retained while scanning for a current-name entry; current-name
// success returns immediately, while a later tar error can fail after a legacy
// file was written.
//
// Matching tar entries with declared size above 500 MiB are rejected and copies
// are bounded. The direct-copy path reads at most that limit and can succeed
// with truncated oversized data because it does not inspect an extra byte. Copy
// or close failures can leave a partial destination; Apply attempts
// staging-directory cleanup afterward, while direct callers own cleanup.
func (s *BinaryInstaller) extractBinary(archivePath, destPath string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	// Close the archive after gzip or tar processing has returned. File-close
	// failure is suppressed and does not remove a destination already written by
	// extraction.
	defer func() { _ = f.Close() }()

	var reader io.Reader = f

	// Suffix spelling enables gzip decoding before format selection. The later
	// .tar substring check still decides whether decoded bytes are traversed as
	// entries or copied directly.
	if strings.HasSuffix(archivePath, ".gz") || strings.HasSuffix(archivePath, ".tar.gz") || strings.HasSuffix(archivePath, ".tgz") {
		gzr, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		// Close the gzip wrapper before the underlying file through defer order.
		// Wrapper close errors are suppressed; actual decompression reads determine
		// extraction failures.
		defer func() { _ = gzr.Close() }()
		reader = gzr
	}

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

			baseName := filepath.Base(hdr.Name)

			// Entry paths never become output paths, but names containing adjacent dots
			// are rejected even for skipped entries. This spelling check also rejects
			// benign names containing that substring.
			if strings.Contains(hdr.Name, "..") {
				return fmt.Errorf("path traversal attempt detected: %s", hdr.Name)
			}

			// Only regular tar payloads can supply the fixed destination binary.
			// Directories and special entries are skipped after the entry-name check has
			// accepted their spelling.
			if hdr.Typeflag != tar.TypeReg {
				continue
			}

			if baseName == "tokenrouter" || baseName == "tokenrouter.exe" || baseName == "sub2api" || baseName == "sub2api.exe" {
				// Bound one matching executable by its uncompressed byte size.
				const maxBinarySize = 500 * 1024 * 1024
				if hdr.Size > maxBinarySize {
					return fmt.Errorf("binary too large: %d bytes (max %d)", hdr.Size, maxBinarySize)
				}

				out, err := os.Create(destPath)
				if err != nil {
					return err
				}

				// The declared tar size was checked before opening the destination. Bound
				// copied bytes as well, keeping one selected executable within the
				// extraction cap.
				limited := io.LimitReader(tr, maxBinarySize)
				if _, err := io.Copy(out, limited); err != nil {
					// Release the failed output handle before returning its copy error. Close
					// failure is discarded, and the partly written destination remains until
					// its caller cleans staging.
					_ = out.Close()
					return err
				}
				if err := out.Close(); err != nil {
					return err
				}
				if baseName == "tokenrouter" || baseName == "tokenrouter.exe" {
					return nil
				}
				// Keep this legacy payload at the fixed destination while traversal
				// continues. A later tokenrouter entry overwrites it; tar errors still fail
				// the operation after this write.
				legacyExtracted = true
			}
		}
		// Clean end-of-archive keeps the already written legacy executable when no
		// current-name entry returned. An intervening tar-read failure instead exits
		// through its earlier error path.
		if legacyExtracted {
			return nil
		}
		return fmt.Errorf("binary not found in archive")
	}

	// The direct path caps bytes without reading beyond the limit. Oversized input
	// can therefore produce a successful truncated destination when its copy and
	// close complete.
	const maxBinarySize = 500 * 1024 * 1024
	out, err := os.Create(destPath)
	if err != nil {
		return err
	}

	limited := io.LimitReader(reader, maxBinarySize)
	if _, err := io.Copy(out, limited); err != nil {
		// Release the partially written direct-copy output before returning its copy
		// error. Its close result is suppressed and destination cleanup remains with
		// the caller.
		_ = out.Close()
		return err
	}
	return out.Close()
}
