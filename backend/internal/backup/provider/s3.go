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

const (
	s3UploadPartSizeMB     = 64
	s3UploadConcurrency    = 4
	s3MinUploadPartSizeMB  = 5
	s3MaxUploadPartSizeMB  = 128
	s3MinUploadConcurrency = 1
	s3MaxUploadConcurrency = 8
	s3MaxUploadParts       = 10000
	s3UploadFailTimeout    = 30 * time.Second

	backupStreamPartSizeBytes = 16 * 1024 * 1024
	backupStreamConcurrency   = 1
)

// S3BackupStore implements backup.BackupObjectStore using AWS S3 compatible storage.
type S3BackupStore struct {
	client            *s3.Client
	bucket            string
	uploadConcurrency int
	uploadPartSizeMB  int
	uploadMode        string
}

type contextAwareReader struct {
	ctx    context.Context
	reader io.Reader
}

type s3UploadProgressListener struct {
	uploaded   atomic.Int64
	onProgress func(uploadedBytes int64)
}

// NewS3BackupStoreFactory returns a BackupObjectStoreFactory that creates S3-backed stores.
func NewS3BackupStoreFactory() backup.BackupObjectStoreFactory {
	return func(ctx context.Context, cfg *backup.BackupS3Config) (backup.BackupObjectStore, error) {
		region := cfg.Region
		if region == "" {
			region = "auto" // Default region for Cloudflare R2
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

func (s *S3BackupStore) Upload(ctx context.Context, key string, body io.Reader, contentType string) (int64, error) {
	if s.uploadMode == backup.BackupS3UploadModeMultipart {
		return s.uploadBackupMultipart(ctx, key, body, contentType)
	}
	return s.uploadBackupSpooledPut(ctx, key, body, contentType)
}

// uploadBackupMultipart bounds buffering with small parts and one concurrent upload for unknown-length streams.
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
		uploaded = *out.ContentLength
	}
	return uploaded, nil
}

// uploadBackupSpooledPut stages input in a private temporary file for a known-length PutObject request.
func (s *S3BackupStore) uploadBackupSpooledPut(ctx context.Context, key string, body io.Reader, contentType string) (sizeBytes int64, err error) {
	tmp, err := os.CreateTemp("", "tokenrouter-backup-upload-*.tmp")
	if err != nil {
		return 0, fmt.Errorf("create backup upload temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	written, err := io.Copy(tmp, &contextAwareReader{ctx: ctx, reader: body})
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

// Read checks cancellation before each read so disk spooling observes task deadlines.
func (r *contextAwareReader) Read(p []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.reader.Read(p)
	}
}

// UploadFile streams large files using the configured multipart buffer sizes.
func (s *S3BackupStore) UploadFile(ctx context.Context, key string, body io.Reader, contentType string) (int64, error) {
	return s.UploadFileWithProgress(ctx, key, body, contentType, nil)
}

// UploadSized sends a known-length part with PutObject, avoiding a second temporary copy.
func (s *S3BackupStore) UploadSized(ctx context.Context, key string, body io.Reader, contentType string, sizeBytes int64) (int64, error) {
	if sizeBytes < 0 {
		return 0, fmt.Errorf("backup upload size must not be negative")
	}
	if err := s.putObject(ctx, key, body, contentType, sizeBytes); err != nil {
		return 0, err
	}
	return sizeBytes, nil
}

// UploadFileWithProgress uploads parts concurrently through transfermanager and reports uploaded bytes.
func (s *S3BackupStore) UploadFileWithProgress(ctx context.Context, key string, body io.Reader, contentType string, onProgress func(uploadedBytes int64)) (int64, error) {
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
		uploaded = *out.ContentLength
	}
	if onProgress != nil {
		onProgress(uploaded)
	}
	return uploaded, nil
}

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

func (s *S3BackupStore) Delete(ctx context.Context, key string) error {
	finish := servertiming.ObserveDependency(ctx, "s3")
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	})
	finish()
	return err
}

func (s *S3BackupStore) PresignURL(ctx context.Context, key string, expiry time.Duration) (string, error) {
	presignClient := s3.NewPresignClient(s.client)
	// Attachment disposition makes navigation to the URL start a download.
	// The frontend can use the current tab and avoid popup blocking.
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

func (l *s3UploadProgressListener) OnObjectBytesTransferred(_ context.Context, event *transfermanager.ObjectBytesTransferredEvent) {
	if l == nil || event == nil {
		return
	}
	uploaded := event.BytesTransferred
	l.uploaded.Store(uploaded)
	if l.onProgress != nil {
		l.onProgress(uploaded)
	}
}

func (l *s3UploadProgressListener) UploadedBytes() int64 {
	if l == nil {
		return 0
	}
	return l.uploaded.Load()
}

func normalizeS3UploadConcurrency(count int) int {
	if count <= 0 {
		count = s3UploadConcurrency
	}
	if count < s3MinUploadConcurrency {
		return s3MinUploadConcurrency
	}
	if count > s3MaxUploadConcurrency {
		return s3MaxUploadConcurrency
	}
	return count
}

func normalizeS3UploadPartSizeMB(sizeMB int) int {
	if sizeMB <= 0 {
		sizeMB = s3UploadPartSizeMB
	}
	if sizeMB < s3MinUploadPartSizeMB {
		return s3MinUploadPartSizeMB
	}
	if sizeMB > s3MaxUploadPartSizeMB {
		return s3MaxUploadPartSizeMB
	}
	return sizeMB
}
