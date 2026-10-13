package batchimage_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/batchimage"
)

// Indexed custom IDs select image parts while the caller owns the returned
// download permit. These cases inspect decoded bytes, permit release,
// filename conversion, owner and state rejection, ZIP contents and mixed
// provider envelopes using in-memory result streams and archives.

// The encoded first-image sample makes it possible to compare downloaded
// bytes and detect accidental payload echo in diagnostics.
const batchImageDownloadTestBase64 = "Zmlyc3Q="

// TestBatchImageDownloadService_OpenItemContent opens a later image part and
// closes its returned Reader repeatedly. It checks decoded bytes, response
// metadata and one permit release, then exercises owner, state, item and
// index failures.
func TestBatchImageDownloadService_OpenItemContent(t *testing.T) {
	ctx := context.Background()

	t.Run("streams image bytes with safe headers data", func(t *testing.T) {
		svc, _, limiter := newTestBatchImageDownloadService()

		stream, err := svc.OpenItemContent(ctx, testBatchImageOwner(), "imgbatch_download", "cover/../001", 1)
		require.NoError(t, err)
		defer func() { require.NoError(t, stream.Reader.Close()) }()

		body, err := io.ReadAll(stream.Reader)
		require.NoError(t, err)
		require.Equal(t, []byte("second"), body)
		require.Equal(t, "image/jpeg", stream.ContentType)
		require.Equal(t, "cover___001.jpg", stream.Filename)
		require.Equal(t, 1, limiter.acquireCount)
		require.Zero(t, limiter.releaseCount)
		require.NoError(t, stream.Reader.Close())
		require.Equal(t, 1, limiter.releaseCount)
	})

	tests := []struct {
		name   string                          // scenario display name
		mutate func(*fakeBatchImageRepository) // scenario setup mutation
		id     string                          // requested batch identity
		item   string                          // requested custom identity
		index  int                             // requested image offset
		want   error                           // expected observable result
	}{
		{name: "non_owner", id: "imgbatch_download", item: "cover/../001", mutate: func(r *fakeBatchImageRepository) {
			v := int64(999)
			r.jobs["imgbatch_download"].APIKeyID = &v
		}, want: batchimage.ErrBatchImageJobNotFound},
		{name: "not_completed", id: "imgbatch_download", item: "cover/../001", mutate: func(r *fakeBatchImageRepository) {
			r.jobs["imgbatch_download"].Status = batchimage.BatchImageJobStatusRunning
		}, want: batchimage.ErrBatchImageNotReady},
		{name: "output_deleted", id: "imgbatch_download", item: "cover/../001", mutate: func(r *fakeBatchImageRepository) {
			r.jobs["imgbatch_download"].Status = batchimage.BatchImageJobStatusOutputDeleted
		}, want: batchimage.ErrBatchImageOutputDeleted},
		{name: "missing_item", id: "imgbatch_download", item: "missing", want: batchimage.ErrBatchImageItemNotFound},
		{name: "failed_item", id: "imgbatch_download", item: "bad", want: batchimage.ErrBatchImageItemFailed},
		{name: "out_of_range", id: "imgbatch_download", item: "cover/../001", index: 2, want: batchimage.ErrBatchImageItemImageIndexOutOfRange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo, _ := newTestBatchImageDownloadService()
			if tt.mutate != nil {
				tt.mutate(repo)
			}

			got, err := svc.OpenItemContent(ctx, testBatchImageOwner(), tt.id, tt.item, tt.index)
			require.Nil(t, got)
			require.ErrorIs(t, err, tt.want)
			require.NotContains(t, err.Error(), batchImageDownloadTestBase64)
			require.NotContains(t, err.Error(), "providers/")
			require.NotContains(t, err.Error(), "gs://")
		})
	}
}

// TestBatchImageDownloadService_StreamZip builds an archive from successful
// indexed items and one provider failure. It checks decoded files, manifest
// metadata, sanitized errors and permit release, plus limiter and item-cap
// rejection before output.
func TestBatchImageDownloadService_StreamZip(t *testing.T) {
	ctx := context.Background()

	t.Run("streams zip with images manifest and errors", func(t *testing.T) {
		svc, _, limiter := newTestBatchImageDownloadService()
		var buf bytes.Buffer

		result, err := svc.StreamZip(ctx, testBatchImageOwner(), "imgbatch_download", batchimage.BatchImageZipOptions{}, &buf)
		require.NoError(t, err)
		require.Equal(t, 3, result.FileCount)
		require.Equal(t, 1, limiter.acquireCount)
		require.Equal(t, 1, limiter.releaseCount)

		files := readZipFiles(t, buf.Bytes())
		require.Equal(t, []byte("first"), files["images/cover___001.png"])
		require.Equal(t, []byte("second"), files["images/cover___001_2.jpg"])
		require.Equal(t, []byte("third"), files["images/ok_2.webp"])
		require.Contains(t, files, "manifest.json")
		require.Contains(t, files, "errors.json")

		zipText := string(bytes.Join(mapValues(files), []byte("\n")))
		require.NotContains(t, zipText, batchImageDownloadTestBase64)
		require.NotContains(t, zipText, "provider_job_name")
		require.NotContains(t, zipText, "provider_input_ref")
		require.NotContains(t, zipText, "gcs_output_uri")
		require.NotContains(t, zipText, "provider_id")
		require.NotContains(t, zipText, "providers/")
		require.NotContains(t, zipText, "gs://")

		var manifest struct {
			Files []struct { // completed archive file metadata
				CustomID   string `json:"custom_id"`   // submitted item identity
				Filename   string `json:"filename"`    // sanitized archive image name
				MimeType   string `json:"mime_type"`   // image MIME type
				ImageIndex int    `json:"image_index"` // zero based image index
			} `json:"files"`
		}
		require.NoError(t, json.Unmarshal(files["manifest.json"], &manifest))
		require.Len(t, manifest.Files, 3)
		require.Equal(t, "images/cover___001_2.jpg", manifest.Files[1].Filename)
		require.Equal(t, 1, manifest.Files[1].ImageIndex)

		var errorsJSON []map[string]string
		require.NoError(t, json.Unmarshal(files["errors.json"], &errorsJSON))
		require.Len(t, errorsJSON, 1)
		require.Equal(t, "bad", errorsJSON[0]["custom_id"])
		require.Equal(t, "SAFETY_BLOCKED", errorsJSON[0]["code"])
	})

	t.Run("limiter denial returns public limit error", func(t *testing.T) {
		svc, _, limiter := newTestBatchImageDownloadService()
		limiter.deny = true
		var buf bytes.Buffer

		result, err := svc.StreamZip(ctx, testBatchImageOwner(), "imgbatch_download", batchimage.BatchImageZipOptions{}, &buf)
		require.Nil(t, result)
		require.ErrorIs(t, err, batchimage.ErrBatchImageDownloadLimited)
		require.Empty(t, buf.Bytes())
	})

	t.Run("rejects too many zip items before opening output", func(t *testing.T) {
		svc, repo, _ := newTestBatchImageDownloadService()
		repo.jobs["imgbatch_download"].SuccessCount = 3
		svc.Options.MaxItems = 1
		var buf bytes.Buffer

		result, err := svc.StreamZip(ctx, testBatchImageOwner(), "imgbatch_download", batchimage.BatchImageZipOptions{}, &buf)
		require.Nil(t, result)
		require.ErrorIs(t, err, batchimage.ErrBatchImageZipTooManyItems)
		require.Empty(t, buf.Bytes())
	})
}

// TestExtractBatchImagePartsFromResultLine feeds supported identity,
// inline-image and failure envelope spellings into download extraction. It
// checks retained image metadata and error categories, and rejects missing
// custom IDs without echoing the encoded sample.
func TestExtractBatchImagePartsFromResultLine(t *testing.T) {
	tests := []struct {
		name      string // scenario display name
		line      string // provider JSON input
		wantID    string // expected custom identity
		wantMime  string // expected image MIME
		wantError string // expected failure category
	}{
		{name: "inlineData_mimeType_response", line: `{"key":"a","response":{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + batchImageDownloadTestBase64 + `"}}]}}]}}`, wantID: "a", wantMime: "image/png"},
		{name: "inline_data_mime_type_top_level", line: `{"custom_id":"b","candidates":[{"content":{"parts":[{"inline_data":{"mime_type":"image/jpeg","data":"` + batchImageDownloadTestBase64 + `"}}]}}]}`, wantID: "b", wantMime: "image/jpeg"},
		{name: "status_failure", line: `{"key":"c","status":{"code":"INVALID_ARGUMENT","message":"bad prompt"}}`, wantID: "c", wantError: "INVALID_ARGUMENT"},
		{name: "error_failure", line: `{"key":"d","error":{"code":"SAFETY","message":"blocked"}}`, wantID: "d", wantError: "SAFETY_BLOCKED"},
		{name: "empty_output", line: `{"key":"e","response":{"candidates":[{"content":{"parts":[{"text":"none"}]}}]}}`, wantID: "e", wantError: "EMPTY_IMAGE_OUTPUT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := batchimage.ExtractBatchImagePartsFromResultLine([]byte(tt.line))
			require.NoError(t, err)
			require.Equal(t, tt.wantID, got.CustomID)
			if tt.wantMime != "" {
				require.Len(t, got.Images, 1)
				require.Equal(t, tt.wantMime, got.Images[0].MimeType)
				require.NotEmpty(t, got.Images[0].Base64Data)
			}
			if tt.wantError != "" {
				require.Equal(t, tt.wantError, got.ErrorCode)
			}
		})
	}

	_, err := batchimage.ExtractBatchImagePartsFromResultLine([]byte(`{"response":{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + batchImageDownloadTestBase64 + `"}}]}}]}}`))
	require.Error(t, err)
	require.NotContains(t, err.Error(), batchImageDownloadTestBase64)
}

// TestBatchImageDownloadFilenames uses path-shaped and quote-containing
// custom names in filename and header helpers. The assertions protect the
// exact sanitized image filename and quoted attachment header emitted for
// these fixtures.
func TestBatchImageDownloadFilenames(t *testing.T) {
	require.Equal(t, "___secret_name.png", batchimage.BatchImageSafeDownloadFilename("../../secret\nname", "png"))
	require.Equal(t, `attachment; filename="cover_001.png"`, batchimage.BatchImageContentDispositionAttachment(`cover"001.png`))
}

// TestExtractBatchImagePartsFromResultLine_MixedEnvelopes combines nested and
// top-level candidates with malformed and empty image parts. It checks
// ordered accepted images, trimmed values and deferred base64 validation
// while excluding ignored parts.
func TestExtractBatchImagePartsFromResultLine_MixedEnvelopes(t *testing.T) {
	line := `{"key":"mixed","response":{"candidates":[null,{}, {"content":{"parts":[
		null,{"text":"none"},{"inlineData":{"mimeType":"text/plain","data":"dGV4dA=="}},
		{"inlineData":{"mimeType":"image/png","data":" \t "}},
		{"inlineData":{},"inline_data":{"mime_type":"image/png","data":"c2tpcA=="}},
		{"inlineData":{"mimeType":" Image/JPEG ","data":" invalid base64 "}},
		{"inline_data":{"mime_type":"image/webp","data":" c2Vjb25k "}}
	]}}]},"candidates":[{"content":{"parts":[
		{"inlineData":{"mimeType":" ","mime_type":" image/png ","data":" dGhpcmQ= "}}
	]}}]}`
	got, err := batchimage.ExtractBatchImagePartsFromResultLine([]byte(line))
	require.NoError(t, err)
	require.Equal(t, "mixed", got.CustomID)
	require.Empty(t, got.ErrorCode)
	require.Equal(t, []batchimage.BatchImageInlineImage{
		{MimeType: "Image/JPEG", Extension: "jpg", Base64Data: "invalid base64"},
		{MimeType: "image/webp", Extension: "webp", Base64Data: "c2Vjb25k"},
		{MimeType: "image/png", Extension: "png", Base64Data: "dGhpcmQ="},
	}, got.Images)
}

// TestExtractBatchImageInlineImages_EmptyInputReturnsNil supplies absent and
// incompatible candidate inputs before an empty response envelope. It checks
// nil image slices and the EMPTY_IMAGE_OUTPUT category used by failed
// download extraction.
func TestExtractBatchImageInlineImages_EmptyInputReturnsNil(t *testing.T) {
	for _, raw := range []any{nil, map[string]any{}, []any{}, []any{nil, map[string]any{"content": nil}}} {
		require.Nil(t, batchimage.ExtractBatchImageInlineImages(raw))
	}
	got, err := batchimage.ExtractBatchImagePartsFromResultLine([]byte(`{"key":"empty","response":{"candidates":[]},"candidates":null}`))
	require.NoError(t, err)
	require.Nil(t, got.Images)
	require.Equal(t, "EMPTY_IMAGE_OUTPUT", got.ErrorCode)
}
