package batchimage

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Batch downloads read provider JSONL using indexed custom IDs and return
// decoded images through the application. A single-item download validates
// ownership, completed state and image index before opening output, then
// transfers its per-user permit to the returned Reader. ZIP downloads hold
// the permit during their scan and archive writing.
//
// The ZIP path reconciles successful indexed items with provider rows, lists
// missing or undecodable results in errors.json and records completed files
// in manifest.json. Its item cap applies to successful custom IDs, while each
// row can produce several image files. A byte writer caps compressed output,
// while the scan checks its duration context between returned rows. Filename
// conversion removes path-shaped base characters before entries are created,
// and the read-closer wrapper releases a single-item permit once even when
// consumers close it repeatedly.

// Download defaults cap successful ZIP items, compressed output and scan
// duration, while the scanner cap bounds inline-image JSONL records.
const (
	DefaultBatchImageZipMaxItems      = 200
	DefaultBatchImageZipMaxBytes      = 512 * 1024 * 1024
	DefaultBatchImageDownloadDuration = 10 * time.Minute

	BatchImageDownloadScannerMaxLineBytes = 16 * 1024 * 1024
)

// ErrBatchImageDownloadSizeExceeded identifies compressed-output rejection so
// StreamZip can return the public download-too-large category.
var ErrBatchImageDownloadSizeExceeded = errors.New("batch image download size limit exceeded")

// DownloadOptions bounds successful ZIP items, compressed output bytes and
// scan duration.
type DownloadOptions struct {
	MaxItems int           // successful ZIP item cap
	MaxBytes int64         // compressed ZIP byte cap
	Duration time.Duration // ZIP scan duration budget
}

// Download authorizes batch result access and manages provider streams with
// per-user permits.
type Download struct {
	Repo            BatchImageRepository                                         // authorized result metadata reader
	ResolveProvider func(context.Context, *BatchImageJob) (BoundProvider, error) // job bound download provider resolver
	Limiter         BatchImageDownloadLimiter                                    // optional per user slot limiter
	Options         DownloadOptions                                              // archive workload and output limits
}

// BatchImageDownloadLimiter supplies per-user admission slots for item and
// archive downloads.
type BatchImageDownloadLimiter interface {
	// Acquire reserves a download slot for the given user identity and operation
	// kind. The caller releases the returned permit after a ZIP call finishes or
	// transfers that responsibility to a single-item Reader.
	Acquire(ctx context.Context, userID string, kind string) (BatchImageDownloadPermit, error)
}

// BatchImageDownloadPermit releases the slot held while a download consumer
// uses its result.
type BatchImageDownloadPermit interface {
	// Release returns the per-user download slot after its consumer finishes.
	// Production permits share the first release result across repeated calls,
	// allowing response cleanup to close a successfully returned Reader more
	// than once.
	Release(ctx context.Context) error
}

// BatchImageContentStream returns one decoded image and safe response
// metadata to its consumer.
type BatchImageContentStream struct {
	// Reader retains the successful item download permit until Close. Callers
	// close it after reading or abandoning the response and inspect any returned
	// release error; a failed decrement leaves Redis expiry to recover capacity.
	Reader        io.ReadCloser // decoded image content reader
	ContentType   string        // response image MIME type
	Filename      string        // sanitized download file name
	ContentLength *int64        // optional decoded byte length
}

// BatchImageZipOptions carries public archive preferences accepted by the
// download operation.
type BatchImageZipOptions struct {
	// Status and IncludeManifest are retained request preferences. Current
	// StreamZip selects successful images and always writes manifest.json and
	// errors.json; MaxItems controls its successful custom-ID cap.
	Status          string // accepted item status preference
	MaxItems        int    // successful ZIP item cap
	IncludeManifest bool   // accepted manifest preference flag
}

// BatchImageZipResult reports completed image files and public archive
// diagnostic counts.
type BatchImageZipResult struct {
	FileCount  int // completed archive image files
	ErrorCount int // public archive diagnostic count
}

// BatchImageLineImages retains decoded row identity and encoded images for
// download selection.
type BatchImageLineImages struct {
	CustomID     string                  // submitted item identity
	Images       []BatchImageInlineImage // encoded row image parts
	ErrorCode    string                  // stable failure category
	ErrorMessage string                  // failure diagnostic text
}

// BatchImageInlineImage retains one provider image part before lazy base64
// decoding.
type BatchImageInlineImage struct {
	MimeType   string // image MIME type
	Extension  string // normalized image filename extension
	Base64Data string // base64
}

// BatchImageDownloadLimitWriter counts compressed archive bytes while
// enforcing a positive write cap.
type BatchImageDownloadLimitWriter struct {
	// w, limit and written describe one archive output stream. Write counts
	// actual accepted bytes, rejects a whole chunk that would exceed limit, and
	// expects serial use.
	w       io.Writer // underlying compressed output writer
	limit   int64     // positive compressed byte cap
	written int64     // accepted compressed byte total
}

// BatchImageZipManifest describes the job and completed image files included
// in an archive.
type BatchImageZipManifest struct {
	BatchID      string                      `json:"batch_id"`      // public batch identifier
	Model        string                      `json:"model"`         // client requested model
	ItemCount    int                         `json:"item_count"`    // expanded request count
	SuccessCount int                         `json:"success_count"` // successful item count
	FailCount    int                         `json:"fail_count"`    // failed item count
	Files        []BatchImageZipManifestFile `json:"files"`         // completed archive file metadata
}

// BatchImageZipManifestFile identifies one completed archive image and its
// source custom ID.
type BatchImageZipManifestFile struct {
	CustomID   string `json:"custom_id"`   // submitted item identity
	Filename   string `json:"filename"`    // sanitized download file name
	MimeType   string `json:"mime_type"`   // image MIME type
	ImageIndex int    `json:"image_index"` // zero based image part index
}

// BatchImageZipError describes a public failure or missing result inside an
// archive.
type BatchImageZipError struct {
	CustomID string `json:"custom_id"` // submitted item identity
	Code     string `json:"code"`      // public failure code
	Message  string `json:"message"`   // public diagnostic text
}

// BatchImagePermitReadCloser couples an image Reader to one download permit
// release.
type BatchImagePermitReadCloser struct {
	io.Reader // decoded image data source
	// permit, once and err coordinate the Reader's retained download slot. Close
	// attempts release once and shares the first result with later close calls.
	permit BatchImageDownloadPermit // retained per user download slot
	once   sync.Once                // single permit release coordination
	err    error                    // first permit release result
}

// Write forwards a complete chunk when it fits the positive compressed-output
// byte limit. Oversized chunks return ErrBatchImageDownloadSizeExceeded
// before writing; accepted writes count the underlying writer's reported
// bytes, including partial writes.
func (w *BatchImageDownloadLimitWriter) Write(p []byte) (int, error) {
	if w == nil || w.w == nil {
		return 0, io.ErrClosedPipe
	}
	// The ZIP writer can emit large chunks, so reject this entire write before
	// the compressed output would exceed its request budget.
	if w.limit > 0 && w.written+int64(len(p)) > w.limit {
		return 0, ErrBatchImageDownloadSizeExceeded
	}
	n, err := w.w.Write(p)
	w.written += int64(n)
	return n, err
}

// OpenItemContent opens one zero-based image of an owner-authorized completed
// batch. The indexed item must be successful and contain the requested index
// before a download permit is acquired. Provider output is scanned for the
// custom ID, then its requested part is decoded lazily.
//
// The provider stream closes within this call after the encoded row has been
// retained. On failure the method attempts permit release; success transfers that
// permit into the returned Reader, which the caller closes after consumption.
// The returned stream has no ContentLength value, and ZIP byte and duration
// limits are applied by StreamZip rather than this item path.
func (s *Download) OpenItemContent(ctx context.Context, owner BatchImageOwner, batchID string, customID string, imageIndex int) (*BatchImageContentStream, error) {
	if imageIndex < 0 {
		return nil, ErrBatchImageItemImageIndexOutOfRange
	}
	job, err := s.GetCompletedJob(ctx, owner, batchID)
	if err != nil {
		return nil, err
	}
	item, err := s.Repo.GetBatchImageItemForDownload(ctx, job.BatchID, customID)
	if err != nil {
		return nil, err
	}
	if item.Status != BatchImageItemStatusSuccess {
		return nil, ErrBatchImageItemFailed
	}
	if imageIndex >= item.ImageCount {
		return nil, ErrBatchImageItemImageIndexOutOfRange
	}

	permit, err := s.AcquirePermit(ctx, owner.UserID, "item")
	if err != nil {
		return nil, err
	}
	releasePermit := true
	// Failures before returning a Reader leave permit ownership here; release it
	// during unwinding so unsuccessful lookups do not retain a download slot.
	defer func() {
		if releasePermit && permit != nil {
			_ = permit.Release(ctx)
		}
	}()

	platform, err := s.ResolveProvider(ctx, job)
	if err != nil {
		return nil, err
	}
	r, _, err := platform.OpenResult(ctx, job)
	if err != nil {
		return nil, ErrBatchImageResultMissing.WithCause(err)
	}
	// The provider stream is scanned within this call; close it after extracting
	// encoded data while the returned decoder retains separate permit ownership.
	defer func() { _ = r.Close() }()

	line, err := FindBatchImageLineImages(r, item.CustomID)
	if err != nil {
		return nil, err
	}
	if imageIndex >= len(line.Images) {
		return nil, ErrBatchImageItemImageIndexOutOfRange
	}
	image := line.Images[imageIndex]
	if strings.TrimSpace(image.Base64Data) == "" {
		return nil, ErrBatchImageResultMissing
	}
	contentType := strings.TrimSpace(image.MimeType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	extension := strings.TrimSpace(image.Extension)
	if extension == "" {
		extension = BatchImageFileExtension(contentType)
	}
	if extension == "" {
		extension = "bin"
	}

	reader := base64.NewDecoder(base64.StdEncoding, strings.NewReader(image.Base64Data))
	// The provider stream has been scanned into encoded text; transfer the
	// permit to the returned decoder wrapper so consumption controls its
	// release.
	releasePermit = false
	return &BatchImageContentStream{
		Reader:      &BatchImagePermitReadCloser{Reader: reader, permit: permit},
		ContentType: contentType,
		Filename:    BatchImageSafeDownloadFilename(item.CustomID, extension),
	}, nil
}

// StreamZip writes an archive for the owner-authorized completed batch and
// returns file and error counts. MaxItems is capped by the service setting
// and checked against both the job count and the actual successful item
// listing before provider output opens. Failed items are included as public
// diagnostics.
//
// The acquired permit and provider stream remain owned by this call.
// Successful custom IDs select image rows for WriteZipImages; the archive
// always includes manifest.json and errors.json. The configured byte cap
// measures compressed output including archive metadata. The scan checks its
// duration context after each returned row; that deadline does not interrupt
// blocked provider reads or image copying. Errors can leave already written bytes in the
// supplied writer, so callers use the returned error to determine whether the
// archive completed.
func (s *Download) StreamZip(ctx context.Context, owner BatchImageOwner, batchID string, opts BatchImageZipOptions, w io.Writer) (*BatchImageZipResult, error) {
	job, err := s.GetCompletedJob(ctx, owner, batchID)
	if err != nil {
		return nil, err
	}
	maxItems := opts.MaxItems
	if cap := s.MaxZipItems(); maxItems <= 0 || maxItems > cap {
		// Client max_items cannot raise the administrator-configured ZIP limit.
		maxItems = cap
	}
	if job.SuccessCount > maxItems {
		return nil, ErrBatchImageZipTooManyItems
	}
	successItems, err := s.Repo.ListBatchImageItemsForDownload(ctx, job.BatchID, BatchImageItemStatusSuccess, maxItems+1)
	if err != nil {
		return nil, err
	}
	if len(successItems) > maxItems {
		return nil, ErrBatchImageZipTooManyItems
	}
	failedItems, err := s.Repo.ListBatchImageItemsForDownload(ctx, job.BatchID, BatchImageItemStatusFailed, maxItems)
	if err != nil {
		return nil, err
	}

	permit, err := s.AcquirePermit(ctx, owner.UserID, "zip")
	if err != nil {
		return nil, err
	}
	if permit != nil {
		// ZIP consumption completes within this call, so return its per-user permit
		// after archive writing or any earlier error has finished.
		defer func() { _ = permit.Release(ctx) }()
	}

	platform, err := s.ResolveProvider(ctx, job)
	if err != nil {
		return nil, err
	}
	r, _, err := platform.OpenResult(ctx, job)
	if err != nil {
		return nil, ErrBatchImageResultMissing.WithCause(err)
	}
	// Archive scanning owns this opened provider stream; close it after writing
	// or any earlier error before returning its permit to the caller.
	defer func() { _ = r.Close() }()

	// Check the derived deadline between returned rows. OpenResult already uses
	// the original request context, so this budget cannot interrupt a blocked read.
	streamCtx := ctx
	cancel := func() {}
	if d := s.MaxDownloadDuration(); d > 0 {
		streamCtx, cancel = context.WithTimeout(ctx, d)
	}
	defer cancel()

	// Count compressed ZIP output, including metadata and final directory
	// records, so the archive limit applies to bytes actually sent to the
	// caller.
	limitedWriter := &BatchImageDownloadLimitWriter{w: w, limit: s.MaxDownloadBytes()}
	zipWriter := zip.NewWriter(limitedWriter)
	// Image, manifest and error writes each attempt archive close after failure.
	// Discard close errors there so the stopped phase's write error determines
	// the response category; normal finalization still checks its own close result.
	result, manifestFiles, zipErrors, err := s.WriteZipImages(streamCtx, zipWriter, r, successItems)
	if err != nil {
		_ = zipWriter.Close()
		if errors.Is(err, ErrBatchImageDownloadSizeExceeded) {
			return result, ErrBatchImageDownloadTooLarge.WithCause(err)
		}
		return result, ErrBatchImageDownloadFailed.WithCause(err)
	}
	zipErrors = append(zipErrors, BatchImageZipErrorsFromItems(failedItems)...)
	if err := WriteBatchImageZipJSON(zipWriter, "manifest.json", BatchImageZipManifest{
		BatchID:      job.BatchID,
		Model:        BatchImageRequestedModel(job),
		ItemCount:    job.ItemCount,
		SuccessCount: job.SuccessCount,
		FailCount:    job.FailCount,
		Files:        manifestFiles,
	}); err != nil {
		_ = zipWriter.Close()
		if errors.Is(err, ErrBatchImageDownloadSizeExceeded) {
			return result, ErrBatchImageDownloadTooLarge.WithCause(err)
		}
		return result, ErrBatchImageDownloadFailed.WithCause(err)
	}
	if err := WriteBatchImageZipJSON(zipWriter, "errors.json", zipErrors); err != nil {
		_ = zipWriter.Close()
		if errors.Is(err, ErrBatchImageDownloadSizeExceeded) {
			return result, ErrBatchImageDownloadTooLarge.WithCause(err)
		}
		return result, ErrBatchImageDownloadFailed.WithCause(err)
	}
	result.ErrorCount = len(zipErrors)
	// Final archive records can still exceed the byte budget; check Close before
	// reporting the ZIP complete to the response caller.
	if err := zipWriter.Close(); err != nil {
		if errors.Is(err, ErrBatchImageDownloadSizeExceeded) {
			return result, ErrBatchImageDownloadTooLarge.WithCause(err)
		}
		return result, ErrBatchImageDownloadFailed.WithCause(err)
	}
	return result, nil
}

// WriteZipImages scans provider rows and writes images for the supplied
// successful custom IDs into an open ZIP writer. It returns partial counters,
// manifest rows and item errors together with any scan or archive-entry
// creation error. The caller owns and finalizes the archive.
//
// Every selected ID begins in a missing set and leaves that set when a
// matching row is found. A row with no images records an item error.
// Image-copy failures record IMAGE_DECODE_FAILED and continue, so an entry
// can contain partial bytes before that error is reported. Only completed
// copies increment FileCount and enter the manifest; sorted unmatched IDs
// become RESULT_MISSING diagnostics.
func (s *Download) WriteZipImages(ctx context.Context, zipWriter *zip.Writer, resultReader io.Reader, successItems []*BatchImageItem) (*BatchImageZipResult, []BatchImageZipManifestFile, []BatchImageZipError, error) {
	successByID := make(map[string]*BatchImageItem, len(successItems))
	missing := make(map[string]struct{}, len(successItems))
	for _, item := range successItems {
		if item == nil {
			continue
		}
		successByID[item.CustomID] = item
		missing[item.CustomID] = struct{}{}
	}
	scanner := bufio.NewScanner(resultReader)
	// Encoded images can enlarge individual JSONL records; reject a row above
	// the scan cap while retaining the counters and archive bytes already
	// produced.
	scanner.Buffer(make([]byte, 0, 64*1024), BatchImageDownloadScannerMaxLineBytes)

	result := &BatchImageZipResult{}
	var manifestFiles []BatchImageZipManifestFile
	var zipErrors []BatchImageZipError
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return result, manifestFiles, zipErrors, err
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		images, err := ExtractBatchImagePartsFromResultLine([]byte(line))
		if err != nil {
			return result, manifestFiles, zipErrors, err
		}
		item := successByID[images.CustomID]
		if item == nil {
			continue
		}
		delete(missing, images.CustomID)
		if len(images.Images) == 0 {
			zipErrors = append(zipErrors, BatchImageZipError{CustomID: images.CustomID, Code: "EMPTY_IMAGE_OUTPUT", Message: "platform response contained no image output"})
			continue
		}
		for idx, image := range images.Images {
			extension := image.Extension
			if extension == "" {
				extension = "bin"
			}
			filename := BatchImageZipImageFilename(item.CustomID, idx, extension)
			entry, err := zipWriter.CreateHeader(&zip.FileHeader{Name: filename, Method: zip.Deflate})
			if err != nil {
				return result, manifestFiles, zipErrors, err
			}
			decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(image.Base64Data))
			// A failed image copy can leave partial entry bytes; record a public item
			// error and retain only fully copied images in the manifest.
			if _, err := io.Copy(entry, decoder); err != nil {
				zipErrors = append(zipErrors, BatchImageZipError{CustomID: item.CustomID, Code: "IMAGE_DECODE_FAILED", Message: "image data could not be decoded"})
				continue
			}
			result.FileCount++
			manifestFiles = append(manifestFiles, BatchImageZipManifestFile{
				CustomID:   item.CustomID,
				Filename:   filename,
				MimeType:   image.MimeType,
				ImageIndex: idx,
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return result, manifestFiles, zipErrors, err
	}
	missingIDs := make([]string, 0, len(missing))
	for customID := range missing {
		missingIDs = append(missingIDs, customID)
	}
	sort.Strings(missingIDs)
	for _, customID := range missingIDs {
		zipErrors = append(zipErrors, BatchImageZipError{CustomID: customID, Code: "RESULT_MISSING", Message: "platform result was not found for item"})
	}
	return result, manifestFiles, zipErrors, nil
}

// GetCompletedJob reads the owner-authorized batch and requires its completed
// status. output_deleted returns the deleted-output category and other stages
// return not-ready. A nil service or repository returns the download-failed
// category before lookup.
func (s *Download) GetCompletedJob(ctx context.Context, owner BatchImageOwner, batchID string) (*BatchImageJob, error) {
	if s == nil || s.Repo == nil {
		return nil, ErrBatchImageDownloadFailed
	}
	job, err := s.Repo.GetBatchImageJobForDownload(ctx, owner.UserID, owner.APIKeyID, batchID)
	if err != nil {
		return nil, err
	}
	switch job.Status {
	case BatchImageJobStatusCompleted:
		return job, nil
	case BatchImageJobStatusOutputDeleted:
		return nil, ErrBatchImageOutputDeleted
	default:
		return nil, ErrBatchImageNotReady
	}
}

// AcquirePermit obtains the optional per-user limiter permit for the
// requested download kind. Missing limiters return nil successfully; limiter
// rejection or failure becomes the public download-limited category for the
// caller.
func (s *Download) AcquirePermit(ctx context.Context, userID int64, kind string) (BatchImageDownloadPermit, error) {
	if s == nil || s.Limiter == nil {
		return nil, nil
	}
	permit, err := s.Limiter.Acquire(ctx, fmt.Sprintf("%d", userID), kind)
	if err != nil {
		// Limiter availability and exhausted capacity both prevent a download slot.
		// Keep the admission category while retaining non-capacity causes for diagnosis.
		if infraerrors.CategoryOf(err) == infraerrors.CategoryTooManyRequests {
			return nil, ErrBatchImageDownloadLimited
		}
		return nil, ErrBatchImageDownloadLimited.WithCause(err)
	}
	return permit, nil
}

// MaxZipItems returns the configured positive limit on successful custom IDs
// in one ZIP request. Missing or nonpositive settings use two hundred, and a
// client MaxItems value cannot raise this service cap.
func (s *Download) MaxZipItems() int {
	if s != nil && s.Options.MaxItems > 0 {
		return s.Options.MaxItems
	}
	return DefaultBatchImageZipMaxItems
}

// MaxDownloadBytes returns the configured positive compressed archive byte
// cap for ZIP requests. Missing or nonpositive settings use 512 MiB, and the
// limiting writer also counts manifest and ZIP metadata bytes.
func (s *Download) MaxDownloadBytes() int64 {
	if s != nil && s.Options.MaxBytes > 0 {
		return s.Options.MaxBytes
	}
	return DefaultBatchImageZipMaxBytes
}

// MaxDownloadDuration returns the configured positive scan duration for a ZIP
// request. Missing or nonpositive settings use ten minutes; StreamZip applies
// that context while it processes provider result rows.
func (s *Download) MaxDownloadDuration() time.Duration {
	if s != nil && s.Options.Duration > 0 {
		return s.Options.Duration
	}
	return DefaultBatchImageDownloadDuration
}

// ExtractBatchImagePartsFromResultLine reads a provider JSON object and
// retains its custom ID and encoded image parts for download. Supported
// identity fields use the same precedence as indexing, and malformed JSON or
// missing identity returns the parse category.
//
// Download concatenates accepted response.candidates images with top-level
// candidate images in that order. This differs from indexing's choice of one
// nonempty envelope, so the indexed image count still limits single-item
// requests. When no image parts exist, provider status and error maps precede
// the response-shaped empty-output diagnosis. Encoded data remains
// unvalidated until a later decoder consumes it.
func ExtractBatchImagePartsFromResultLine(line []byte) (*BatchImageLineImages, error) {
	var obj map[string]any
	if err := json.Unmarshal(line, &obj); err != nil {
		return nil, ErrBatchImageIndexParseFailed.WithCause(err)
	}
	customID := BatchImageFirstNonEmptyString(
		BatchImageMapString(obj, "key"),
		BatchImageMapString(obj, "custom_id"),
		BatchImageMapString(obj, "customId"),
		BatchImageNestedString(obj, "request", "key"),
	)
	if customID == "" {
		return nil, ErrBatchImageIndexParseFailed.WithCause(fmt.Errorf("missing custom id"))
	}
	out := &BatchImageLineImages{CustomID: customID}
	// Keep images from both accepted envelopes in their source order. Single-item
	// callers still limit the requested index using the independently stored count.
	out.Images = append(out.Images, ExtractBatchImageInlineImages(BatchImageNestedAny(obj, "response", "candidates"))...)
	out.Images = append(out.Images, ExtractBatchImageInlineImages(obj["candidates"])...)
	if len(out.Images) > 0 {
		return out, nil
	}
	if code, message, ok := BatchImageFailureFromProviderFields(obj); ok {
		out.ErrorCode = code
		out.ErrorMessage = TruncateBatchImageMessage(message, BatchImageMaxErrorMessageLength)
		return out, nil
	}
	if _, hasResponse := obj["response"]; hasResponse || BatchImageHasCandidates(obj) {
		out.ErrorCode = "EMPTY_IMAGE_OUTPUT"
		out.ErrorMessage = "platform response contained no image output"
		return out, nil
	}
	out.ErrorCode = "PROVIDER_ITEM_FAILED"
	out.ErrorMessage = "platform result line contained no image output"
	return out, nil
}

// ExtractBatchImageInlineImages collects trimmed MIME and encoded data from
// accepted inline image parts in traversal order. Empty or incompatible input
// returns nil, and decoding remains the later download consumer's
// responsibility.
func ExtractBatchImageInlineImages(raw any) []BatchImageInlineImage {
	var images []BatchImageInlineImage
	visitBatchImageInlineImages(raw, func(mime, data string) {
		images = append(images, BatchImageInlineImage{
			MimeType:   mime,
			Extension:  BatchImageFileExtension(mime),
			Base64Data: data,
		})
	})
	return images
}

// FindBatchImageLineImages scans bounded JSONL records until the requested
// custom ID is found. Parse failures in earlier rows abort the lookup; a
// matched failure becomes item-failed, and missing rows or output become
// result-missing.
func FindBatchImageLineImages(r io.Reader, customID string) (*BatchImageLineImages, error) {
	scanner := bufio.NewScanner(r)
	// Single-image lookup scans encoded rows before selecting one custom ID; the
	// per-line cap bounds a provider row even when another item is requested.
	scanner.Buffer(make([]byte, 0, 64*1024), BatchImageDownloadScannerMaxLineBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parsed, err := ExtractBatchImagePartsFromResultLine([]byte(line))
		if err != nil {
			return nil, err
		}
		if parsed.CustomID != customID {
			continue
		}
		if len(parsed.Images) == 0 {
			if parsed.ErrorCode != "" {
				return nil, ErrBatchImageItemFailed
			}
			return nil, ErrBatchImageResultMissing
		}
		return parsed, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, ErrBatchImageDownloadFailed.WithCause(err)
	}
	return nil, ErrBatchImageResultMissing
}

// BatchImageSafeDownloadFilename joins a sanitized custom-ID base and
// normalized extension. An empty accepted extension becomes bin, and the
// returned filename is used for individual image downloads and ZIP entries.
func BatchImageSafeDownloadFilename(customID, extension string) string {
	base := SanitizeBatchImageFilenameBase(customID)
	extension = SanitizeBatchImageFilenameExtension(extension)
	if extension == "" {
		extension = "bin"
	}
	return base + "." + extension
}

// BatchImageContentDispositionAttachment formats an attachment header from a
// download filename. It replaces quotes and backslashes and sanitizes the
// basename, retaining its extension; callers provide the extension through
// the safe filename builder.
func BatchImageContentDispositionAttachment(filename string) string {
	filename = strings.ReplaceAll(filename, "\\", "_")
	filename = strings.ReplaceAll(filename, `"`, "_")
	filename = SanitizeBatchImageFilenameBase(strings.TrimSuffix(filename, filepath.Ext(filename))) + filepath.Ext(filename)
	return `attachment; filename="` + filename + `"`
}

// SanitizeBatchImageFilenameBase converts a custom ID into a bounded image
// filename base. Path separators, controls and unsupported characters become
// underscores; leading or repeated dot patterns are normalized, and unusable
// values become image.
func SanitizeBatchImageFilenameBase(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "image"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == 0:
			_ = b.WriteByte('_')
		case unicode.IsControl(r):
			_ = b.WriteByte('_')
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.':
			_, _ = b.WriteRune(r)
		default:
			_ = b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), ". ")
	for strings.Contains(out, "..") {
		out = strings.ReplaceAll(out, "..", "_")
	}
	out = strings.Trim(out, ". ")
	if out == "" {
		out = "image"
	}
	// Cap the sanitized base in bytes before adding its extension, keeping
	// generated download filenames bounded.
	if len(out) > 120 {
		out = strings.TrimRight(out[:120], ". ")
	}
	if out == "" {
		out = "image"
	}
	return out
}

// SanitizeBatchImageFilenameExtension trims and lowercases a suffix, removes
// its first leading dot and retains Unicode letters or digits. The result is
// capped at twelve bytes, and an empty suffix is handled by the caller.
func SanitizeBatchImageFilenameExtension(extension string) string {
	extension = strings.TrimPrefix(strings.TrimSpace(strings.ToLower(extension)), ".")
	var b strings.Builder
	for _, r := range extension {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			_, _ = b.WriteRune(r)
		}
	}
	out := b.String()
	// Keep accepted extensions byte-bounded before they are appended to item
	// filenames and archive entry names.
	if len(out) > 12 {
		out = out[:12]
	}
	return out
}

// BatchImageZipImageFilename places a sanitized image filename below the
// archive images/ directory. The first zero-based image keeps the base name;
// later images receive a one-based suffix before final sanitation. A base near
// the 120-byte cap can lose that suffix when the safe builder truncates again,
// and this helper does not check filename collisions.
func BatchImageZipImageFilename(customID string, imageIndex int, extension string) string {
	base := SanitizeBatchImageFilenameBase(customID)
	if imageIndex > 0 {
		base = fmt.Sprintf("%s_%d", base, imageIndex+1)
	}
	return "images/" + BatchImageSafeDownloadFilename(base, extension)
}

// WriteBatchImageZipJSON creates a deflated ZIP entry and encodes the
// supplied value as indented JSON. The caller supplies an internal entry name
// and finalizes the archive; creation and encoding errors propagate.
func WriteBatchImageZipJSON(zipWriter *zip.Writer, name string, value any) error {
	entry, err := zipWriter.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(entry)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

// BatchImageZipErrorsFromItems converts nonnil indexed failure items into
// public archive diagnostics. Stored error text is sanitized through the
// public-message rule, and nil items are skipped while input order is
// retained.
func BatchImageZipErrorsFromItems(items []*BatchImageItem) []BatchImageZipError {
	out := make([]BatchImageZipError, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		out = append(out, BatchImageZipError{
			CustomID: item.CustomID,
			Code:     BatchImageDerefString(item.ErrorCode),
			Message:  SanitizeBatchImagePublicMessage(BatchImageDerefString(item.ErrorMessage)),
		})
	}
	return out
}

// Close attempts the retained download permit's release once through a
// background context and returns that result. Repeated calls share the stored
// result. The caller manages any closable underlying Reader separately;
// OpenItemContent supplies a decoder with no additional Close operation.
func (r *BatchImagePermitReadCloser) Close() error {
	r.once.Do(func() {
		if r.permit != nil {
			r.err = r.permit.Release(context.Background())
		}
	})
	return r.err
}
