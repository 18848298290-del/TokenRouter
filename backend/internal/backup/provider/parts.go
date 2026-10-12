package provider

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/backup"
)

const defaultBackupPartSizeBytes int64 = 4 * 1024 * 1024 * 1024

// BackupPart describes a gzip archive segment; StorageKey is current and S3Key supports legacy clients.
type BackupPart = backup.BackupPart

type localBackupPart struct {
	Index     int
	Path      string
	SizeBytes int64
	SHA256    string
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

// spoolNextBackupPart writes one bounded part and peeks for remaining input.
// The caller removes the returned temporary file after use.
func spoolNextBackupPart(ctx context.Context, src *bufio.Reader, index int, partSize int64) (part localBackupPart, hasPart bool, hasMore bool, err error) {
	if partSize <= 0 {
		return part, false, false, errors.New("backup part size must be positive")
	}
	if index <= 0 {
		return part, false, false, errors.New("backup part index must be positive")
	}

	tmp, err := os.CreateTemp("", "tokenrouter-backup-part-*.tmp")
	if err != nil {
		return part, false, false, fmt.Errorf("create backup part: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	hash := sha256.New()
	written, copyErr := io.CopyN(io.MultiWriter(tmp, hash), &contextReader{ctx: ctx, reader: src}, partSize)
	if copyErr != nil && !errors.Is(copyErr, io.EOF) {
		cleanup()
		return part, false, false, fmt.Errorf("write backup part %d: %w", index, copyErr)
	}
	if written == 0 {
		cleanup()
		return part, false, false, nil
	}
	if closeErr := tmp.Close(); closeErr != nil {
		_ = os.Remove(tmpPath)
		return part, false, false, fmt.Errorf("close backup part %d: %w", index, closeErr)
	}

	if _, peekErr := src.Peek(1); peekErr == nil {
		hasMore = true
	} else if !errors.Is(peekErr, io.EOF) {
		_ = os.Remove(tmpPath)
		return part, false, false, fmt.Errorf("inspect backup part %d boundary: %w", index, peekErr)
	}

	return localBackupPart{
		Index:     index,
		Path:      tmpPath,
		SizeBytes: written,
		SHA256:    hex.EncodeToString(hash.Sum(nil)),
	}, true, hasMore, nil
}

// Read checks cancellation before each read so part spooling observes task deadlines.
func (r *contextReader) Read(p []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.reader.Read(p)
	}
}

func buildBackupPartKey(baseKey, backupID string, index int) string {
	root := pathpkg.Dir(strings.TrimSpace(baseKey))
	if root == "." {
		root = ""
	}
	return pathpkg.Join(root, strings.TrimSpace(backupID), fmt.Sprintf("payload.part-%06d", index))
}

func downloadBackupParts(ctx context.Context, objectStore BackupObjectStore, parts []BackupPart) (archivePath string, err error) {
	ordered, err := backup.OrderedBackupParts(parts)
	if err != nil {
		return "", err
	}

	archive, err := os.CreateTemp("", "tokenrouter-backup-restore-*.sql.gz")
	if err != nil {
		return "", fmt.Errorf("create restore archive: %w", err)
	}
	tempPath := archive.Name()
	archivePath = tempPath
	defer func() {
		if closeErr := archive.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close restore archive: %w", closeErr)
		}
		if err != nil {
			_ = cleanupBackupFiles(tempPath)
		}
	}()

	for _, part := range ordered {
		body, downloadErr := objectStore.Download(ctx, backup.BackupPartStorageKey(part))
		if downloadErr != nil {
			return "", fmt.Errorf("download backup part %d: %w", part.Index, downloadErr)
		}
		hash := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(archive, hash), &contextReader{ctx: ctx, reader: body})
		closeErr := body.Close()
		if copyErr != nil {
			return "", fmt.Errorf("read backup part %d: %w", part.Index, copyErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("close backup part %d: %w", part.Index, closeErr)
		}
		if written != part.SizeBytes {
			return "", fmt.Errorf("backup part %d size mismatch: got %d, want %d", part.Index, written, part.SizeBytes)
		}
		actualSHA256 := hex.EncodeToString(hash.Sum(nil))
		if part.SHA256 != "" && !strings.EqualFold(part.SHA256, actualSHA256) {
			return "", fmt.Errorf("backup part %d checksum mismatch", part.Index)
		}
	}
	return archivePath, nil
}

func cleanupBackupFiles(paths ...string) error {
	var errs []error
	for _, filePath := range paths {
		if strings.TrimSpace(filePath) == "" {
			continue
		}
		if err := os.Remove(filePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove %s: %w", filePath, err))
		}
	}
	return errors.Join(errs...)
}
