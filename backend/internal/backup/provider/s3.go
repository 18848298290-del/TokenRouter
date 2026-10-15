package provider

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/TokenFlux/TokenRouter/internal/backup"
	servertiming "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
)

// s3.go uploads backup archives to S3-compatible storage and provides
// download, deletion, and signing operations.
// Unknown-length gzip streams and already sized archive files need different
// upload preparation. Streaming multipart uses fixed small parts with one worker;
// spooled PutObject first measures a temporary file, and known-length segment
// upload sends the supplied reader directly. File transfer settings choose bounded
// part sizes and worker counts separately from the unknown-length streaming limits.
//
// Transfer progress comes from SDK callbacks that can run on concurrent part
// workers. The listener atomically stores each callback's cumulative total, which
// can be observed out of order; it does not enforce monotonic updates. Successful
// transfer returns the larger of the latest observation and SDK content length,
// then file upload emits a final callback. Error results report observed progress,
// which describes successful transfers inside an attempt rather than a completed object.

const (
	// File transfer defaults and bounds limit configured worker counts and part sizes,
	// while the SDK part cap and failure timeout govern multipart completion and cleanup.
	s3UploadPartSizeMB     = 64
	s3UploadConcurrency    = 4
	s3MinUploadPartSizeMB  = 5
	s3MaxUploadPartSizeMB  = 128
	s3MinUploadConcurrency = 1
	s3MaxUploadConcurrency = 8
	s3MaxUploadParts       = 10000
	s3UploadFailTimeout    = 30 * time.Second

	// Unknown-length backup streams use a separate part budget and one worker so
	// normal file-upload concurrency does not multiply buffers on the gzip pipe path.
	backupStreamPartSizeBytes = 16 * 1024 * 1024
	backupStreamConcurrency   = 1
)

// S3BackupStore implements backup.BackupObjectStore using AWS S3 compatible storage.
type S3BackupStore struct {
	client *s3.Client // configured object-storage API client
	bucket string     // archive destination bucket name
	// uploadConcurrency and uploadPartSizeMB configure file transfer workers and MiB
	// part sizes. Unknown-length multipart streams use their separate fixed limits instead.
	uploadConcurrency int // parallel file upload workers
	uploadPartSizeMB  int // MiB
	// uploadMode selects multipart or spooled PutObject for Upload. UploadFile uses
	// transfermanager independently, while UploadSized always sends a known-length PutObject request.
	uploadMode string
}

// s3UploadProgressListener retains the latest cumulative total emitted by transfermanager.
type s3UploadProgressListener struct {
	// uploaded stores callback totals atomically, making reads safe during part work.
	// Concurrent callbacks can arrive out of cumulative order, so this value can move backward.
	uploaded atomic.Int64
	// onProgress executes synchronously in the SDK callback's worker when supplied.
	// Callers handle concurrent updates to shared output and let the callback return;
	// waiting for its enclosing upload here prevents that worker from completing.
	onProgress func(uploadedBytes int64)
}

// NewS3BackupStoreFactory returns a factory for configured object-storage clients.
// Factory calls require nonnil configuration, default empty regions to "auto",
// and normalize transfer limits and upload mode. A configured endpoint is retained
// through its field pointer, so callers keep cfg.Endpoint stable while the client is used.
func NewS3BackupStoreFactory() backup.BackupObjectStoreFactory {
	return func(ctx context.Context, cfg *backup.BackupS3Config) (backup.BackupObjectStore, error) {
		region := cfg.Region
		if region == "" {
			region = "auto"
		}

		awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
			awsconfig.WithRegion(region),
			awsconfig.WithCredentialsProvider(
				credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
			),
		)
		if err != nil {
			return nil, fmt.Errorf("load aws config: %w", err)
		}

		client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
			if cfg.Endpoint != "" {
				o.BaseEndpoint = &cfg.Endpoint
			}
			if cfg.ForcePathStyle {
				o.UsePathStyle = true
			}
			// Archive inputs can be nonseekable streams. Use unsigned-payload signing and
			// request automatic checksums only when required, keeping client preparation compatible with those readers.
			o.APIOptions = append(o.APIOptions, v4.SwapComputePayloadSHA256ForUnsignedPayloadMiddleware)
			o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		})

		return &S3BackupStore{
			client:            client,
			bucket:            cfg.Bucket,
			uploadConcurrency: normalizeS3UploadConcurrency(cfg.UploadConcurrency),
			uploadPartSizeMB:  normalizeS3UploadPartSizeMB(cfg.UploadPartSizeMB),
			uploadMode:        backup.NormalizeBackupS3UploadMode(cfg.UploadMode),
		}, nil
	}
}

// Upload sends archive bytes using the store's selected stream upload mode.
// Multipart mode delegates to fixed streaming transfer limits; other modes spool
// the source before known-length PutObject. The caller supplies a readable source
// and handles its lifetime around the upload operation.
func (s *S3BackupStore) Upload(ctx context.Context, key string, body io.Reader, contentType string) (int64, error) {
	if s.uploadMode == backup.BackupS3UploadModeMultipart {
		return s.uploadBackupMultipart(ctx, key, body, contentType)
	}
	return s.uploadBackupSpooledPut(ctx, key, body, contentType)
}

// uploadBackupMultipart uploads an unknown-length stream with sixteen-MiB parts
// and one transfer worker, returning its observed byte count and operation error.
// Source lifetime belongs to the archive caller, and SDK cancellation governs the transfer.
//
// Progress tracks cumulative SDK transfer events. On success, content length
// can supply a larger final size than the listener's latest observed value;
// on failure, that latest value is returned for diagnostics and does not establish
// a completed object. The separate stream limits keep configured file concurrency
// from changing how many multipart workers consume this gzip upload path.
func (s *S3BackupStore) uploadBackupMultipart(ctx context.Context, key string, body io.Reader, contentType string) (int64, error) {
	progress := &s3UploadProgressListener{}
	uploader := transfermanager.New(s.client, func(o *transfermanager.Options) {
		o.PartSizeBytes = backupStreamPartSizeBytes
		o.MultipartUploadThreshold = backupStreamPartSizeBytes
		o.Concurrency = backupStreamConcurrency
		o.MaxUploadParts = s3MaxUploadParts
		o.FailTimeout = s3UploadFailTimeout
		o.ObjectProgressListeners.Register(progress)
	})
	finish := servertiming.ObserveDependency(ctx, "s3")
	out, err := uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket:      &s.bucket,
		Key:         &key,
		Body:        body,
		ContentType: &contentType,
	})
	finish()
	if err != nil {
		return progress.UploadedBytes(), fmt.Errorf("S3 backup multipart upload: %w", err)
	}
	uploaded := progress.UploadedBytes()
	if out.ContentLength != nil && *out.ContentLength > uploaded {
		// A returned content length can cover callbacks observed out of order or
		// an incomplete latest sample; use that larger successful result when reporting archive size.
		uploaded = *out.ContentLength
	}
	return uploaded, nil
}

// uploadBackupSpooledPut copies the source into a temporary file, rewinds it,
// and sends its measured length through PutObject. Failures return zero even after
// partial spooling; the original source remains the caller's resource, and temporary
// close and removal are attempted on every return while their errors are suppressed.
func (s *S3BackupStore) uploadBackupSpooledPut(ctx context.Context, key string, body io.Reader, contentType string) (sizeBytes int64, err error) {
	tmp, err := os.CreateTemp("", "tokenrouter-backup-upload-*.tmp")
	if err != nil {
		return 0, fmt.Errorf("create backup upload temp file: %w", err)
	}
	tmpPath := tmp.Name()
	// Dispose of the spool after either upload or input failure. Cleanup is best
	// effort so the primary transfer result remains visible even if temporary-file removal fails.
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	written, err := io.Copy(tmp, &contextReader{ctx: ctx, reader: body})
	if err != nil {
		return 0, fmt.Errorf("spool backup upload: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return 0, fmt.Errorf("rewind backup upload temp file: %w", err)
	}
	if err := s.putObject(ctx, key, tmp, contentType, written); err != nil {
		return 0, err
	}
	return written, nil
}

// UploadFile sends a file reader through configured transfermanager limits without
// a progress callback. It shares UploadFileWithProgress's returned size and partial
// progress errors, and uses that path independently of the store's stream upload mode.
func (s *S3BackupStore) UploadFile(ctx context.Context, key string, body io.Reader, contentType string) (int64, error) {
	return s.UploadFileWithProgress(ctx, key, body, contentType, nil)
}

// UploadSized sends a caller-measured segment through PutObject and returns that
// supplied size after success. Negative sizes are rejected and zero is allowed;
// callers provide a reader matching sizeBytes because this path performs no independent spool or measurement.
func (s *S3BackupStore) UploadSized(ctx context.Context, key string, body io.Reader, contentType string, sizeBytes int64) (int64, error) {
	if sizeBytes < 0 {
		return 0, fmt.Errorf("backup upload size must not be negative")
	}
	if err := s.putObject(ctx, key, body, contentType, sizeBytes); err != nil {
		return 0, err
	}
	return sizeBytes, nil
}

// UploadFileWithProgress transfers a reader with normalized file part sizes and
// concurrency, reporting cumulative SDK totals through an optional callback. Callbacks
// can run on multiple part workers, so their captured shared state needs synchronization.
//
// The listener records the latest callback without enforcing event order. Successful
// completion chooses a larger SDK content length when available and emits the
// final size synchronously after transfermanager returns. Errors return the latest
// observation instead of a final object size. The caller owns the source's lifetime
// and keeps callback state available until the operation returns. Callbacks return
// before the enclosing upload can finish, so they avoid waiting for that same upload's completion.
func (s *S3BackupStore) UploadFileWithProgress(ctx context.Context, key string, body io.Reader, contentType string, onProgress func(uploadedBytes int64)) (int64, error) {
	// Convert configured MiB into the byte-sized part budget consumed by transfermanager.
	partSizeBytes := int64(normalizeS3UploadPartSizeMB(s.uploadPartSizeMB)) * 1024 * 1024
	progress := &s3UploadProgressListener{onProgress: onProgress}
	uploader := transfermanager.New(s.client, func(o *transfermanager.Options) {
		o.PartSizeBytes = partSizeBytes
		o.MultipartUploadThreshold = partSizeBytes
		o.Concurrency = normalizeS3UploadConcurrency(s.uploadConcurrency)
		o.MaxUploadParts = s3MaxUploadParts
		o.FailTimeout = s3UploadFailTimeout
		o.ObjectProgressListeners.Register(progress)
	})
	finish := servertiming.ObserveDependency(ctx, "s3")
	out, err := uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket:      &s.bucket,
		Key:         &key,
		Body:        body,
		ContentType: &contentType,
	})
	finish()
	if err != nil {
		return progress.UploadedBytes(), fmt.Errorf("S3 transfer upload object: %w", err)
	}
	uploaded := progress.UploadedBytes()
	if out.ContentLength != nil && *out.ContentLength > uploaded {
		// Concurrent workers may deliver a lower cumulative sample last. Prefer the
		// successful SDK object's larger content length before emitting the caller's final progress update.
		uploaded = *out.ContentLength
	}
	if onProgress != nil {
		onProgress(uploaded)
	}
	return uploaded, nil
}

// putObject sends one S3 request with the supplied body length and content type.
// It observes request timing and wraps API failures, while body preparation and
// measurement belong to the sized or spooled caller choosing this helper.
func (s *S3BackupStore) putObject(ctx context.Context, key string, body io.Reader, contentType string, contentLength int64) error {
	finish := servertiming.ObserveDependency(ctx, "s3")
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        &s.bucket,
		Key:           &key,
		Body:          body,
		ContentType:   &contentType,
		ContentLength: &contentLength,
	})
	finish()
	if err != nil {
		return fmt.Errorf("S3 PutObject: %w", err)
	}
	return nil
}

// Download opens the requested object and returns the SDK response body for its
// caller to close after reading. Request timing ends when GetObject returns;
// subsequent archive byte consumption retains the response body's own transport and context behavior.
func (s *S3BackupStore) Download(ctx context.Context, key string) (io.ReadCloser, error) {
	finish := servertiming.ObserveDependency(ctx, "s3")
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	})
	finish()
	if err != nil {
		return nil, fmt.Errorf("S3 GetObject: %w", err)
	}
	return result.Body, nil
}

// Delete requests object removal under the supplied context and records request timing.
// It returns the SDK error directly, allowing archive cleanup to retain the key
// and report storage failure when deletion cannot be confirmed.
func (s *S3BackupStore) Delete(ctx context.Context, key string) error {
	finish := servertiming.ObserveDependency(ctx, "s3")
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	})
	finish()
	return err
}

// PresignURL returns a signed object download URL with the requested lifetime
// and attachment disposition using the key's basename. Signing errors are wrapped;
// URL generation delegates endpoint and expiry behavior to the configured SDK presigner.
func (s *S3BackupStore) PresignURL(ctx context.Context, key string, expiry time.Duration) (string, error) {
	presignClient := s3.NewPresignClient(s.client)
	// Sign an attachment response disposition with the basename rather than its
	// object prefix, so clients receive the archive filename when navigating to the download URL.
	disposition := fmt.Sprintf("attachment; filename=%q", path.Base(key))
	result, err := presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket:                     &s.bucket,
		Key:                        &key,
		ResponseContentDisposition: &disposition,
	}, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", fmt.Errorf("presign url: %w", err)
	}
	return result.URL, nil
}

// HeadBucket asks the configured endpoint to validate bucket access and records
// request timing. A successful request returns nil; API failures are wrapped for
// the administrator's storage-connection test response.
func (s *S3BackupStore) HeadBucket(ctx context.Context) error {
	finish := servertiming.ObserveDependency(ctx, "s3")
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: &s.bucket,
	})
	finish()
	if err != nil {
		return fmt.Errorf("S3 HeadBucket failed: %w", err)
	}
	return nil
}

// OnObjectBytesTransferred stores the cumulative SDK event total and invokes the
// optional callback on that event's worker. Nil listeners or events are ignored;
// atomic storage protects the sample, while callbacks may overlap and observe totals out of order.
func (l *s3UploadProgressListener) OnObjectBytesTransferred(_ context.Context, event *transfermanager.ObjectBytesTransferredEvent) {
	if l == nil || event == nil {
		return
	}
	uploaded := event.BytesTransferred
	// Store the event's cumulative total rather than adding it again; the SDK
	// emitter already aggregates completed transfer bytes before dispatching potentially concurrent callback events.
	l.uploaded.Store(uploaded)
	if l.onProgress != nil {
		l.onProgress(uploaded)
	}
}

// UploadedBytes returns the latest atomically stored transfer sample, or zero
// for a nil listener. The sample can regress when concurrent SDK callbacks arrive
// out of order, so successful callers also compare the SDK's final content length.
func (l *s3UploadProgressListener) UploadedBytes() int64 {
	if l == nil {
		return 0
	}
	return l.uploaded.Load()
}

// normalizeS3UploadConcurrency selects four workers for nonpositive configuration
// and bounds positive values to the supported one-to-eight range. File uploads use
// this setting; unknown-length stream uploads keep their independent single-worker limit.
func normalizeS3UploadConcurrency(count int) int {
	if count <= 0 {
		count = s3UploadConcurrency
	}
	if count < s3MinUploadConcurrency {
		// Keep configured file transfers within the supported minimum worker count.
		return s3MinUploadConcurrency
	}
	if count > s3MaxUploadConcurrency {
		// Cap file worker fan-out before transfermanager allocates concurrent upload work.
		return s3MaxUploadConcurrency
	}
	return count
}

// normalizeS3UploadPartSizeMB selects sixty-four MiB for nonpositive configuration
// and clamps positive values to five through one hundred twenty-eight MiB. The
// result becomes a file upload byte budget independently of fixed streaming parts.
func normalizeS3UploadPartSizeMB(sizeMB int) int {
	if sizeMB <= 0 {
		sizeMB = s3UploadPartSizeMB
	}
	if sizeMB < s3MinUploadPartSizeMB {
		// Keep configured file parts above the store's supported multipart size minimum.
		return s3MinUploadPartSizeMB
	}
	if sizeMB > s3MaxUploadPartSizeMB {
		// Bound each configured file part before converting its MiB budget to bytes.
		return s3MaxUploadPartSizeMB
	}
	return sizeMB
}
