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

// One gzip stream can be stored as successive objects when uploads need a known
// content length. Spooling writes one bounded segment and its digest, then peeks
// at the buffered source to distinguish the final segment from a full-sized
// segment with more bytes behind it. The returned temporary file is closed before
// upload so its metadata describes the segment written to that file.
//
// Multipart restoration downloads every ordered segment into one temporary archive,
// checking its byte count and any supplied digest before returning that path to
// the restore caller. Download, read, close, or verification failures abandon the
// assembled file and attempt removal. Cancellation checks run before source reads;
// a source already blocked in Read or the boundary Peek still needs its own
// cancellation or closing behavior to stop promptly.

// defaultBackupPartSizeBytes supplies the four-GiB spool limit selected by
// NewArchive when its caller does not configure a positive archive segment size.
const defaultBackupPartSizeBytes int64 = 4 * 1024 * 1024 * 1024

// BackupPart describes a gzip archive segment; StorageKey is current and S3Key supports legacy clients.
type BackupPart = backup.BackupPart

// localBackupPart describes closed temporary segment bytes ready for object upload.
type localBackupPart struct {
	Index int // one-based stream segment number
	// Path names the closed file created by spoolNextBackupPart. Its caller removes
	// that file after upload or abandons it through the shared temporary-file cleanup helper.
	Path      string
	SizeBytes int64  // bytes
	SHA256    string // SHA-256 hexadecimal segment digest
}

// contextReader checks task cancellation before delegating each underlying source read.
type contextReader struct {
	// ctx is checked before each reader call; reader retains its own blocking behavior.
	// An already active source read cannot be interrupted by this wrapper's next check.
	ctx    context.Context // source read admission context
	reader io.Reader       // underlying archive byte source
}

// spoolNextBackupPart returns one closed temporary segment, whether it contains
// bytes, and whether more input follows. Index and partSize must be positive;
// an empty source succeeds with no segment, and callers remove successful files
// after consuming them. The supplied buffered reader is advanced by this call.
//
// Copying and hashing share one writer so metadata describes the exact segment
// bytes. EOF can finish a short final segment, while a one-byte Peek distinguishes
// an exact-size final segment from one needing another object. Non-EOF copy or
// boundary errors attempt file cleanup before returning their primary error;
// source reads and Peek still depend on the source to unblock during cancellation.
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
		// Close and remove a failed or empty spool as best-effort cleanup. Retain the
		// copy error or empty-source result rather than replacing it with cleanup diagnostics here.
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	hash := sha256.New()
	// Hash the same bytes written to disk so subsequent upload metadata can validate
	// each segment without rereading it, while CopyN keeps this spool within its part budget.
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
		// A failed close leaves this spool unusable for upload. Attempt removal and
		// return the close error, keeping the caller from publishing incomplete temporary-file metadata.
		_ = os.Remove(tmpPath)
		return part, false, false, fmt.Errorf("close backup part %d: %w", index, closeErr)
	}

	// Look beyond this segment without consuming the next byte, so exact-size EOF
	// remains a single part and the next spool starts at its original stream position.
	if _, peekErr := src.Peek(1); peekErr == nil {
		hasMore = true
	} else if !errors.Is(peekErr, io.EOF) {
		// A non-EOF boundary failure cannot establish whether another segment is needed.
		// Attempt removal before returning the boundary failure to the archive writer.
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

// Read checks ctx before calling the underlying reader and returns its context
// error when already cancelled. Once the delegated read begins, its blocking and
// cancellation behavior belongs to that reader, which the archive caller may need to close.
func (r *contextReader) Read(p []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.reader.Read(p)
	}
}

// buildBackupPartKey places a numbered segment beneath the backup ID and the
// base key's directory. It trims supplied key text and uses slash-based object
// paths so multipart objects remain grouped beside the original single-file archive location.
func buildBackupPartKey(baseKey, backupID string, index int) string {
	root := pathpkg.Dir(strings.TrimSpace(baseKey))
	if root == "." {
		root = ""
	}
	return pathpkg.Join(root, strings.TrimSpace(backupID), fmt.Sprintf("payload.part-%06d", index))
}

// downloadBackupParts returns a closed temporary archive after ordered segment
// downloads match their recorded sizes and optional SHA-256 digests. Callers remove
// the successful path after restoration, and use a store whose download bodies can
// unblock when its request context is cancelled.
//
// Metadata validation happens before downloads. Each segment is copied and hashed
// before its body closes, then checked before the next segment is appended. Any
// failure attempts to remove the assembled archive, so the restore caller receives
// success only after every supplied segment and the temporary archive close have
// completed, rather than starting database restoration with an unverified partial stream.
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
	// Preserve close failures as assembly errors and attempt removal on any failure,
	// so a restore caller never treats a failed download or verification as a ready archive.
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
		// Append and hash each downloaded body, then close it before checking metadata;
		// the recorded size is verified after copying rather than acting as a read limit.
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
		// Older metadata can lack a digest. Verify supplied hexadecimal text case
		// insensitively while still enforcing recorded size for every legacy or current segment.
		if part.SHA256 != "" && !strings.EqualFold(part.SHA256, actualSHA256) {
			return "", fmt.Errorf("backup part %d checksum mismatch", part.Index)
		}
	}
	return archivePath, nil
}

// cleanupBackupFiles attempts removal of each nonblank temporary path and joins
// removal failures. Missing files succeed so cleanup can be repeated after an
// earlier path already removed its spool or assembled restoration archive.
func cleanupBackupFiles(paths ...string) error {
	var errs []error
	for _, filePath := range paths {
		if strings.TrimSpace(filePath) == "" {
			continue
		}
		// Earlier cleanup can already have removed a path. Treat that missing file
		// as complete while retaining other filesystem failures for the caller's cleanup diagnostics.
		if err := os.Remove(filePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove %s: %w", filePath, err))
		}
	}
	return errors.Join(errs...)
}
