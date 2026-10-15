package provider

import (
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/backup"
)

// archive.go writes compressed database archives to object storage and
// restores single-object or segmented archives.
// A compressed database dump is produced while storage consumes the gzip stream.
// Successful storage alone does not establish successful export: the producer
// also closes the dumper, whose process exit can report a failure after its output
// was read. Upload failure cancels that producer and closes the pipe's read side
// before waiting for its final result, so compression can stop instead of waiting
// for a consumer that has already returned.
//
// Spooled S3 archives retain a single object when the stream fits one segment.
// Larger streams register each segment's key, size, and digest before upload so
// cleanup can address every planned object after an uncertain storage result.
// Restoration checks all multipart bytes against their metadata before opening
// the assembled gzip stream; single-object restoration streams directly and uses
// body closure to assist cancellation. Temporary removal and progress persistence
// have their own best-effort paths, distinct from mandatory multipart-plan saves.

// Archive storage labels select the spooled S3 path, while the cleanup timeout
// is passed to the service's budget-aware cleanup context factory after write failure.
const (
	BackupStorageTypeS3          = backup.BackupStorageTypeS3          // remote S3 archive storage
	BackupS3UploadModeSpooledPut = backup.BackupS3UploadModeSpooledPut // known-length spooled S3 upload
	backupObjectCleanupTimeout   = 2 * time.Minute
)

type (
	// BackupRecord names the backup module's persisted archive and operation metadata.
	BackupRecord = backup.BackupRecord
	// BackupS3Config supplies the backup module's storage connection and upload settings.
	BackupS3Config = backup.BackupS3Config
	// BackupDumpOptions supplies table-data exclusions for the database export operation.
	BackupDumpOptions = backup.BackupDumpOptions
	// BackupObjectStore names archive storage operations provided to the backup executor.
	BackupObjectStore = backup.BackupObjectStore
	// BackupObjectStoreSizedUploader identifies stores accepting already measured archive segment uploads.
	BackupObjectStoreSizedUploader = backup.BackupObjectStoreSizedUploader
)

// Archive streams backups and keeps each Write call's persistence callback in a separate copy.
type Archive struct {
	dumper        backup.DBDumper // shared database archive executor
	partSizeBytes int64           // maximum spooled segment bytes
	// save belongs to the shallow Archive copy made for each Write invocation.
	// Its callback persists that call's mutable record without rebinding another Write's callback.
	save func(context.Context, *BackupRecord) error
}

// NewArchive binds the supplied database dumper and archive segment budget.
// Nonpositive budgets choose the default four-GiB segment size; construction starts
// no dump, and the caller retains responsibility for shared dumper configuration and lifetime.
func NewArchive(dumper backup.DBDumper, partSize int64) *Archive {
	if partSize <= 0 {
		partSize = defaultBackupPartSizeBytes
	}
	return &Archive{dumper: dumper, partSizeBytes: partSize}
}

// Write compresses a database dump into object storage and returns the archive
// byte result after both upload and producer completion. Callers supply a nonnil
// record, dumper, store, and persistence/cleanup callbacks, keeping this record
// exclusive to its operation while progress and multipart metadata are mutated.
//
// A shallow executor copy keeps the persistence callback local to this call;
// injected dependencies remain shared. Upload and the chosen producer failure are
// joined after upload failure requests cancellation and closes its pipe reader.
// Dumper-close failure is selected when copying and gzip closure have not already failed.
// Initial uploading-progress save errors are ignored, but multipart-plan saves
// precede each upload and can stop execution. Failure cleanup uses the caller's
// context factory and joins object-removal errors with the transfer failures.
func (s *Archive) Write(ctx context.Context, record *BackupRecord, objectStore BackupObjectStore, s3Cfg *BackupS3Config, dumpOptions BackupDumpOptions, save func(context.Context, *BackupRecord) error, cleanup func(time.Duration) (context.Context, context.CancelFunc)) (int64, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Bind this operation's saver to a shallow copy so concurrent calls cannot
	// overwrite one another's callback while still using the injected shared dumper and part budget.
	local := *s
	local.save = save
	s = &local
	dumpReader, err := s.dumper.Dump(ctx, dumpOptions)
	if err != nil {
		return 0, fmt.Errorf("pg_dump failed: %w", err)
	}

	pr, pw := io.Pipe()
	gzipDone := make(chan error, 1)
	// The producer owns dumpReader and the pipe writer until its terminal result;
	// storage consumes the reader concurrently and can close it when an upload fails.
	go func() {
		var gzipErr error
		defer func() {
			// Convert a producer panic into its terminal failure and close the dumper
			// before pipe completion, allowing subprocess exit errors to reach the waiting upload coordinator.
			if recovered := recover(); recovered != nil {
				gzipErr = fmt.Errorf("gzip goroutine panic: %v", recovered)
			}
			if closeErr := dumpReader.Close(); gzipErr == nil && closeErr != nil {
				gzipErr = closeErr
			}
			if gzipErr != nil {
				// Publish producer failure through the pipe before signalling completion, so
				// a storage reader waiting for more compressed bytes can finish with that failure too.
				_ = pw.CloseWithError(gzipErr)
			} else {
				// Successful producer completion exposes EOF to storage before notifying Write;
				// the buffered result channel then permits this goroutine to finish without another reader.
				_ = pw.Close()
			}
			gzipDone <- gzipErr
		}()

		gzWriter := gzip.NewWriter(pw)
		_, gzipErr = io.Copy(gzWriter, dumpReader)
		// Finish the gzip trailer after source copying, preserving the first copy error
		// when trailer writing also fails so the terminal result still describes its initial failure.
		if closeErr := gzWriter.Close(); gzipErr == nil && closeErr != nil {
			gzipErr = closeErr
		}
	}()

	record.Progress = "uploading"
	// Progress persistence is observational here. Continue archive transfer after that
	// save fails; mandatory segment-plan persistence below determines whether multipart uploads may proceed.
	_ = s.save(ctx, record)
	var sizeBytes int64
	if shouldUseBackupParts(record, s3Cfg) {
		sizeBytes, err = s.uploadBackupParts(ctx, record, objectStore, pr)
	} else {
		sizeBytes, err = objectStore.Upload(ctx, backup.RecordEffectiveStorageKey(record), pr, "application/gzip")
	}
	uploadErr := err
	if uploadErr != nil {
		// Stop source production and release a producer blocked on the pipe before
		// waiting for its completion, because the failed storage consumer will no longer drain it.
		cancel()
		_ = pr.CloseWithError(uploadErr)
	}
	gzipErr := <-gzipDone
	switch {
	case uploadErr != nil && gzipErr != nil:
		err = errors.Join(
			fmt.Errorf("backup write failed: %w", uploadErr),
			fmt.Errorf("gzip/dump failed after writer stopped: %w", gzipErr),
		)
	case uploadErr != nil:
		err = fmt.Errorf("backup write failed: %w", uploadErr)
	case gzipErr != nil:
		err = fmt.Errorf("gzip/dump failed: %w", gzipErr)
	}
	if err == nil {
		return sizeBytes, nil
	}

	// A failed attempt can leave objects despite its error. Use the caller's cleanup
	// budget to remove all recorded keys and retain deletion failures in the returned diagnostic.
	cleanupCtx, cleanupCancel := cleanup(backupObjectCleanupTimeout)
	cleanupErr := backup.DeleteBackupObjectKeys(cleanupCtx, objectStore, record)
	cleanupCancel()
	return sizeBytes, errors.Join(err, cleanupErr)
}

// uploadBackupParts spools successive compressed segments and uploads them through
// this call's store and saver. A stream fitting the first segment retains the
// record's single-object key; larger streams replace that key with ordered part metadata.
//
// Each multipart key, size, and digest is saved before its object upload, so
// failure cleanup includes a part whose upload outcome is uncertain. The byte
// result sums recorded sizes of successful parts; the failed part's partial transfer
// count is not included. Temporary removal is attempted after consumption with
// errors suppressed, allowing the next segment to use another bounded spool.
func (s *Archive) uploadBackupParts(ctx context.Context, record *BackupRecord, objectStore BackupObjectStore, src io.Reader) (int64, error) {
	partSize := s.partSizeBytes
	if partSize <= 0 {
		partSize = defaultBackupPartSizeBytes
	}
	// Buffer a small boundary lookahead independently of the configured spool size.
	reader := bufio.NewReaderSize(&contextReader{ctx: ctx, reader: src}, 32*1024)
	firstPart, hasPart, hasMore, err := spoolNextBackupPart(ctx, reader, 1, partSize)
	if err != nil {
		return 0, err
	}
	if !hasPart {
		return 0, errors.New("backup archive is empty")
	}
	// Keep a final cleanup attempt for the first spool, including the single-file
	// branch and early failures; repeated removal succeeds when the loop already disposed of it.
	defer func() { _ = cleanupBackupFiles(firstPart.Path) }()

	if !hasMore {
		// A one-segment archive remains addressable by legacy single-file metadata,
		// while its measured size still allows a direct sized upload through supporting stores.
		sizeBytes, uploadErr := uploadBackupPart(ctx, objectStore, backup.RecordEffectiveStorageKey(record), firstPart, "application/gzip")
		return sizeBytes, uploadErr
	}

	// Multipart metadata replaces single-file keys before its plan is saved. Cleanup
	// and restore then use the registered segment keys instead of a nonexistent combined object.
	baseKey := backup.RecordEffectiveStorageKey(record)
	record.StorageKey = ""
	record.S3Key = ""
	record.Parts = nil
	totalBytes := int64(0)
	part := firstPart
	for {
		key := buildBackupPartKey(baseKey, record.ID, part.Index)
		// Register and persist the candidate object before uploading it, retaining its
		// address for cleanup even when storage reports an error after accepting some or all bytes.
		record.Parts = append(record.Parts, BackupPart{
			Index:      part.Index,
			StorageKey: key,
			S3Key:      key,
			SizeBytes:  part.SizeBytes,
			SHA256:     part.SHA256,
		})
		if err := s.save(ctx, record); err != nil {
			// The segment plan was not confirmed in settings. Attempt local spool removal
			// and stop before upload, retaining earlier successful parts in the returned byte count.
			_ = cleanupBackupFiles(part.Path)
			return totalBytes, fmt.Errorf("save backup part plan: %w", err)
		}
		if _, err := uploadBackupPart(ctx, objectStore, key, part, "application/octet-stream"); err != nil {
			// This object's key is already registered for the enclosing failure cleanup.
			// Attempt spool removal while reporting the upload error and prior successful segment total.
			_ = cleanupBackupFiles(part.Path)
			return totalBytes, fmt.Errorf("upload backup part %d: %w", part.Index, err)
		}
		totalBytes += part.SizeBytes
		// The uploaded segment's metadata now identifies remote bytes. Attempt local
		// spool removal before advancing, retaining the upload result if temporary cleanup itself fails.
		_ = cleanupBackupFiles(part.Path)
		if !hasMore {
			return totalBytes, nil
		}

		part, hasPart, hasMore, err = spoolNextBackupPart(ctx, reader, part.Index+1, partSize)
		if err != nil {
			return totalBytes, err
		}
		if !hasPart {
			return totalBytes, errors.New("backup archive ended before the next planned part")
		}
	}
}

// uploadBackupPart opens a closed spool and uploads its bytes, closing the reader
// on return. Stores implementing sized upload receive its measured length directly;
// other stores use UploadFile, which chooses its own transfer preparation.
func uploadBackupPart(ctx context.Context, objectStore BackupObjectStore, key string, part localBackupPart, contentType string) (int64, error) {
	file, err := os.Open(part.Path)
	if err != nil {
		return 0, fmt.Errorf("open backup part %d: %w", part.Index, err)
	}
	// The upload result is selected before deferred spool-reader closure. Discard
	// a closing error here so the returned diagnostic continues to describe that object transfer.
	defer func() { _ = file.Close() }()
	if uploader, ok := objectStore.(BackupObjectStoreSizedUploader); ok {
		return uploader.UploadSized(ctx, key, file, contentType, part.SizeBytes)
	}
	// Compatibility stores without a sized entry point still consume the same spool
	// through their file-upload method, allowing archive segmentation to retain its existing metadata plan.
	return objectStore.UploadFile(ctx, key, file, contentType)
}

// shouldUseBackupParts reports whether an effective S3 record selects spooled
// PutObject upload with supplied S3 settings. Local storage, missing settings, and
// multipart stream mode keep the ordinary single-object streaming path.
func shouldUseBackupParts(record *BackupRecord, s3Cfg *BackupS3Config) bool {
	return backup.RecordEffectiveStorageType(record) == BackupStorageTypeS3 &&
		s3Cfg != nil &&
		backup.NormalizeBackupS3UploadMode(s3Cfg.UploadMode) == BackupS3UploadModeSpooledPut
}

// restoreBackupArchive opens the assembled archive, decompresses it, and delegates
// SQL restoration to the injected dumper. File and gzip readers close on return;
// callers remove the archive path separately after this operation completes.
func (s *Archive) restoreBackupArchive(ctx context.Context, archivePath string) error {
	archive, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open restore archive: %w", err)
	}
	// Keep the assembled file open through decompression and database input. Deferred
	// file closure is attempted after the restore result, and its error is discarded.
	defer func() { _ = archive.Close() }()
	gzReader, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	// The gzip reader leaves its underlying archive file for the earlier defer.
	// Its own closing error is discarded after the database restoration result has been selected.
	defer func() { _ = gzReader.Close() }()
	if err := s.dumper.Restore(ctx, gzReader); err != nil {
		return fmt.Errorf("pg restore: %w", err)
	}
	return nil
}

// Restore decompresses the supplied record's archive into the injected database
// restore operation. Callers provide a nonnil record, store, and dumper; multipart
// metadata causes all segments to be downloaded and checked before restoration,
// while single-object records stream their downloaded body directly.
//
// Multipart checks enforce order and recorded byte counts, plus supplied digests;
// absent digests skip checksum comparison. Assembly errors stop before the dumper
// starts. Single-object cancellation closes the body through an AfterFunc callback,
// which can overlap deferred closure, so store bodies support those closing calls.
// Restore and input errors are returned, while temporary and reader cleanup errors
// on these deferred paths are suppressed.
func (s *Archive) Restore(ctx context.Context, record *BackupRecord, objectStore BackupObjectStore) error {
	if len(record.Parts) > 0 {
		archivePath, err := downloadBackupParts(ctx, objectStore, record.Parts)
		if err != nil {
			return err
		}
		// Keep the verified assembled file until the dumper finishes reading it, then
		// attempt removal regardless of restore success while retaining the database operation's result.
		defer func() { _ = cleanupBackupFiles(archivePath) }()
		return s.restoreBackupArchive(ctx, archivePath)
	}

	body, err := objectStore.Download(ctx, backup.RecordEffectiveStorageKey(record))
	if err != nil {
		return fmt.Errorf("backup download failed: %w", err)
	}
	// Retain the download body through SQL consumption, then attempt closure while
	// preserving the restore or input result even when that deferred close reports an error.
	defer func() { _ = body.Close() }()
	// Cancellation can arrive while decompression waits for remote input. Close the
	// download body to assist that reader, allowing deferred cleanup to close it again on return.
	stopClose := context.AfterFunc(ctx, func() { _ = body.Close() })
	defer stopClose()

	// Feed decompressed bytes directly to the dumper so single-object restoration
	// avoids staging the complete archive; source errors remain observable through the restore reader.
	gzReader, err := gzip.NewReader(body)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	// Gzip closure leaves the separately owned download body open. Discard its
	// closing error after SQL consumption so the returned outcome describes restoration or input failure.
	defer func() { _ = gzReader.Close() }()

	if err := s.dumper.Restore(ctx, gzReader); err != nil {
		return fmt.Errorf("pg restore: %w", err)
	}

	return nil
}
