package upstream

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strings"

	"github.com/tidwall/sjson"
)

// An image request can keep client prompt and upload data while routing
// selects a different upstream model. JSON requests replace the model path
// directly; multipart requests need a newly framed body because the model is a
// text part among uploads and mask parts. Multipart output receives a fresh
// boundary and a matching content type.
//
// Each parsed part keeps its order and cloned header values, with all model
// text parts receiving the supplied model and a missing text field appended at
// the end. A file part named model is copied as an upload. The standard
// multipart reader handles transfer decoding before copying, so parsed payload
// content is carried into the rebuilt body rather than preserving the original
// wire encoding.

// RewriteImageModel replaces a nonblank trimmed model in JSON or recognized
// multipart input. Blank model returns the supplied body storage and content
// type unchanged; other content types, including MIME parse failures, use JSON
// mutation.
func RewriteImageModel(body []byte, contentType string, model string) ([]byte, string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return body, contentType, nil
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err == nil && strings.EqualFold(mediaType, "multipart/form-data") {
		rewrittenBody, rewrittenType, rewriteErr := RewriteImageMultipartModel(body, contentType, model)
		return rewrittenBody, rewrittenType, rewriteErr
	}
	// A content type that is not successfully recognized as multipart follows
	// JSON mutation. Retain the supplied content type for the forwarding caller
	// while allowing the JSON mutator to report payload errors.
	rewritten, err := sjson.SetBytes(body, "model", model)
	if err != nil {
		return nil, "", fmt.Errorf("rewrite image request model: %w", err)
	}
	return rewritten, contentType, nil
}

// RewriteImageMultipartModel rebuilds multipart input with the supplied model
// text and a new boundary. It requires a parseable content type with a
// nonblank boundary and uses model exactly as supplied.
//
// Every text part whose trimmed name is model is replaced, while file parts
// with that name retain their payload content. If no model text part exists,
// one is appended. Parsed headers and decoded part content are copied in
// source order; quoted-printable handling belongs to multipart.NextPart, and
// part close errors are discarded after read or write handling.
func RewriteImageMultipartModel(body []byte, contentType string, model string) ([]byte, string, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, "", fmt.Errorf("parse multipart content-type: %w", err)
	}
	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return nil, "", fmt.Errorf("multipart boundary is required")
	}

	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	modelWritten := false

	// NextPart supplies parsed headers and transfer-decoded body bytes. Each
	// branch closes the current part after copying or replacing it, keeping
	// cleanup errors separate from the read/write result selected for the caller.
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", fmt.Errorf("read multipart body: %w", err)
		}

		formName := strings.TrimSpace(part.FormName())
		partHeader := CloneMultipartHeader(part.Header)
		target, err := writer.CreatePart(partHeader)
		if err != nil {
			_ = part.Close()
			return nil, "", fmt.Errorf("create multipart part: %w", err)
		}

		// Routing replaces model text fields while retaining a file named model
		// as upload content. Repeated text fields receive the same selected model.
		if formName == "model" && part.FileName() == "" {
			if _, err := target.Write([]byte(model)); err != nil {
				_ = part.Close()
				return nil, "", fmt.Errorf("rewrite multipart model: %w", err)
			}
			modelWritten = true
			_ = part.Close()
			continue
		}
		if _, err := io.Copy(target, part); err != nil {
			_ = part.Close()
			return nil, "", fmt.Errorf("copy multipart part: %w", err)
		}
		_ = part.Close()
	}

	// A request can omit its model text field and rely on routing defaults.
	// Append the selected upstream model after the copied parts in that case.
	if !modelWritten {
		if err := writer.WriteField("model", model); err != nil {
			return nil, "", fmt.Errorf("append multipart model field: %w", err)
		}
	}
	// Closing writes the final delimiter for the newly chosen multipart boundary.
	// Return that complete body together with its generated content type to forwarding.
	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("finalize multipart body: %w", err)
	}
	return buffer.Bytes(), writer.FormDataContentType(), nil
}

// CloneMultipartHeader copies a MIME header map and every value slice so the
// caller can modify them independently. Nil input becomes an allocated empty
// map, and nil value slices become allocated empty slices.
func CloneMultipartHeader(src textproto.MIMEHeader) textproto.MIMEHeader {
	dst := make(textproto.MIMEHeader, len(src))
	for key, values := range src {
		copied := make([]string, len(values))
		copy(copied, values)
		dst[key] = copied
	}
	return dst
}
