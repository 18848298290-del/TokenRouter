package vertex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
)

// batch_client.go accesses Vertex batch jobs and GCS objects and reads result
// objects as a combined JSONL stream.
// Vertex batch jobs refer to JSONL objects stored in GCS and return a prefix
// containing result objects. The HTTP clients use bearer tokens supplied by
// the task owner; job state and managed-object cleanup remain with that
// caller. Object listing follows returned page tokens and filters JSONL names
// on each page.
//
// The combined reader opens one object at a time and inserts a newline between
// adjacent objects so a final line cannot merge with the next object. An
// object returning bytes together with EOF delivers those bytes before the
// separator. Closing stops future reads and closes the active stream; callers
// serialize reads and closing, use nonempty read buffers, and leave the
// borrowed object list unchanged. A zero-length read at an object EOF can
// discard that separating newline.

// VertexBatchClient exchanges Vertex batch job resources using caller-supplied
// access tokens.
type VertexBatchClient interface {
	// CreateBatchPredictionJob submits a job with the supplied bearer token and
	// endpoint identity. It returns the server job resource or an exchange error;
	// task ownership and subsequent polling belong to the caller.
	CreateBatchPredictionJob(ctx context.Context, accessToken string, req VertexCreateBatchPredictionJobRequest) (*VertexBatchPredictionJob, error)
	// GetBatchPredictionJob reads a named job using the supplied bearer token.
	// The returned state is one server observation; callers decide polling
	// cadence and whether the job has reached completion.
	GetBatchPredictionJob(ctx context.Context, accessToken string, name string) (*VertexBatchPredictionJob, error)
	// CancelBatchPredictionJob requests cancellation for a named job using the
	// supplied bearer token. Successful submission reports request acceptance;
	// callers observe subsequent job state to establish task completion.
	CancelBatchPredictionJob(ctx context.Context, accessToken string, name string) error
}

// VertexBatchObjectStore accesses GCS inputs and outputs for a caller-owned
// batch task.
type VertexBatchObjectStore interface {
	// UploadJSONL uploads reader contents to a caller-selected GCS object using
	// its bearer token. Reader ownership and cancellation behavior follow the
	// implementation, and callers manage the stored object lifetime.
	UploadJSONL(ctx context.Context, accessToken string, uri string, r io.Reader) error
	// ListJSONLObjects returns JSONL object URIs under the requested GCS prefix.
	// It reports listing errors to the caller, which selects how those objects
	// belong to the batch task.
	ListJSONLObjects(ctx context.Context, accessToken string, prefixURI string) ([]string, error)
	// OpenObject opens the named object and returns its content type with the
	// stream. The caller closes that stream after consumption; the request
	// context can cancel the download.
	OpenObject(ctx context.Context, accessToken string, uri string) (io.ReadCloser, string, error)
	// DeleteObject removes one GCS object using the supplied bearer token. Errors
	// return to the caller, which decides whether cleanup can be retried or the
	// task requires intervention.
	DeleteObject(ctx context.Context, accessToken string, uri string) error
	// DeletePrefix lists the prefix and deletes each discovered object in order.
	// An error stops cleanup after earlier deletions, so callers handle partial
	// cleanup and retry eligibility.
	DeletePrefix(ctx context.Context, accessToken string, prefixURI string) error
}

// VertexCreateBatchPredictionJobRequest carries the endpoint identity and
// submitted batch job body.
type VertexCreateBatchPredictionJobRequest struct {
	// ProjectID and Location select the submission endpoint and are excluded from
	// the JSON body. The remaining request fields describe the job submitted at
	// that endpoint.
	ProjectID   string `json:"-"`           // endpoint project identifier
	Location    string `json:"-"`           // endpoint location identifier
	DisplayName string `json:"displayName"` // submitted job display name
	Model       string `json:"model"`       // batch publisher model resource
	// stored input instance configuration
	InputConfig VertexBatchInputConfig `json:"inputConfig"`
	// stored prediction output configuration
	OutputConfig VertexBatchOutputConfig `json:"outputConfig"`
	// optional instance key configuration
	InstanceConfig *VertexBatchInstanceConfig `json:"instanceConfig,omitempty"`
}

// VertexBatchInputConfig describes the input instance format and its GCS
// source.
type VertexBatchInputConfig struct {
	// input instance serialization format
	InstancesFormat string               `json:"instancesFormat"`
	GCSSource       VertexBatchGCSSource `json:"gcsSource"` // input object URI collection
}

// VertexBatchGCSSource identifies stored objects supplying the batch input
// instances.
type VertexBatchGCSSource struct {
	URIs []string `json:"uris"` // input GCS object URIs
}

// VertexBatchOutputConfig describes the prediction format and its output
// destination.
type VertexBatchOutputConfig struct {
	// prediction output serialization format
	PredictionsFormat string `json:"predictionsFormat"`
	// prediction object prefix destination
	GCSDestination VertexBatchGCSDestination `json:"gcsDestination"`
}

// VertexBatchGCSDestination holds the GCS prefix containing batch output
// objects.
type VertexBatchGCSDestination struct {
	OutputURIPrefix string `json:"outputUriPrefix"` // GCS output object prefix
}

// VertexBatchInstanceConfig identifies the input field used to correlate batch
// results.
type VertexBatchInstanceConfig struct {
	KeyField string `json:"keyField"` // input result-correlation field name
}

// VertexBatchPredictionJob represents the server-reported job state and output
// location.
type VertexBatchPredictionJob struct {
	Name         string                  `json:"name"`         // server job resource name
	DisplayName  string                  `json:"displayName"`  // server job display name
	State        string                  `json:"state"`        // server job lifecycle state
	OutputConfig VertexBatchOutputConfig `json:"outputConfig"` // server prediction output location
	Error        *VertexBatchJobError    `json:"error"`        // optional server job failure
}

// VertexBatchJobError retains the server-reported structured failure for a
// batch job.
type VertexBatchJobError struct {
	Code    any    `json:"code"`    // server error code value
	Message string `json:"message"` // server error message text
	Status  string `json:"status"`  // server symbolic error status
}

// vertexCombinedJSONLReadCloser concatenates ordered object streams with
// deferred separating newlines.
type vertexCombinedJSONLReadCloser struct {
	// ctx, accessToken, objects, and store are fixed after construction; objects
	// shares the caller slice. index, current, needBoundary, and closed change
	// during serialized Read and Close calls.
	ctx          context.Context        // object download cancellation context
	accessToken  string                 // object request bearer token
	objects      []string               // borrowed ordered object URIs
	store        VertexBatchObjectStore // object stream opening implementation
	index        int                    // next object slice position
	current      io.ReadCloser          // active object response body
	needBoundary bool                   // pending object separating newline
	closed       bool                   // future read rejection flag
}

// VertexBatchHTTPClient sends authenticated Vertex job requests through a
// supplied HTTP client.
type VertexBatchHTTPClient struct {
	baseURL string // normalized API endpoint prefix
	// client is retained directly for HTTP exchanges. Changes to its configuration
	// affect subsequent requests, so callers configure it before sharing this wrapper.
	client *http.Client
}

// VertexGCSObjectStore sends GCS media and object requests through its HTTP
// client.
type VertexGCSObjectStore struct {
	baseURL string // normalized API endpoint prefix
	// client is retained directly for HTTP exchanges. Changes to its configuration
	// affect subsequent requests, so callers configure it before sharing this wrapper.
	client *http.Client
}

// VertexAPIError retains the HTTP status and selected server error
// description.
type VertexAPIError struct {
	StatusCode int    // upstream HTTP status code
	Code       string // structured upstream symbolic status
	Message    string // selected upstream error text
}

// NormalizeVertexBatchModelPath trims surrounding whitespace and slashes from
// a model resource. Publisher and project resource prefixes pass through;
// other names receive the Google publisher-model prefix, including an empty
// supplied name.
func NormalizeVertexBatchModelPath(model string) string {
	model = strings.Trim(strings.TrimSpace(model), "/")
	if strings.HasPrefix(model, "publishers/") || strings.HasPrefix(model, "projects/") {
		return model
	}
	return "publishers/google/models/" + model
}

// BuildVertexBatchPredictionJobsEndpoint constructs an escaped project and
// location endpoint for job submission. Blank location selects global; a
// supplied base URL replaces the default host after trimming and is validated
// by the caller.
func BuildVertexBatchPredictionJobsEndpoint(baseURL, projectID, location string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	location = strings.TrimSpace(location)
	if projectID == "" {
		return "", errors.New("vertex project_id is required")
	}
	if location == "" {
		location = "global"
	}
	if !vertexLocationPattern.MatchString(location) {
		return "", fmt.Errorf("invalid vertex location: %s", location)
	}
	if strings.TrimSpace(baseURL) != "" {
		return strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/v1/projects/" + url.PathEscape(projectID) + "/locations/" + url.PathEscape(location) + "/batchPredictionJobs", nil
	}
	host := fmt.Sprintf("%s-aiplatform.googleapis.com", location)
	if location == "global" {
		host = "aiplatform.googleapis.com"
	}
	return fmt.Sprintf("https://%s/v1/projects/%s/locations/%s/batchPredictionJobs", host, url.PathEscape(projectID), url.PathEscape(location)), nil
}

// Read supplies ordered object bytes with a newline between adjacent object
// streams. Callers serialize access and treat an object-open error as a failed
// combined read; the object index has already advanced on that path. Read
// buffers must be nonempty to retain every inter-object separator.
//
// EOF closes the active object before the next opens. When EOF accompanies
// bytes, Read returns those bytes successfully and defers the separator to
// another call, so the separator cannot overwrite the final bytes. A closed
// reader returns ErrClosedPipe, and finishing the object list returns EOF.
func (r *vertexCombinedJSONLReadCloser) Read(p []byte) (int, error) {
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	if r.needBoundary {
		if len(p) == 0 {
			return 0, nil
		}
		p[0] = '\n'
		r.needBoundary = false
		return 1, nil
	}
	for {
		if r.current == nil {
			if r.index >= len(r.objects) {
				return 0, io.EOF
			}
			// Advance the object cursor before opening; an open failure returns
			// immediately, so another Read begins with the following object rather than
			// retrying this one.
			obj := r.objects[r.index]
			r.index++
			rc, _, err := r.store.OpenObject(r.ctx, r.accessToken, obj)
			if err != nil {
				return 0, err
			}
			r.current = rc
		}
		n, err := r.current.Read(p)
		// Release an exhausted object before opening its successor. Preserve any
		// bytes returned with EOF and emit the inter-object newline on a later read
		// when necessary.
		if err == io.EOF {
			_ = r.current.Close()
			r.current = nil
			if r.index < len(r.objects) {
				if n > 0 {
					r.needBoundary = true
					return n, nil
				}
				if len(p) == 0 {
					return 0, nil
				}
				p[0] = '\n'
				return 1, nil
			}
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

// Close rejects subsequent reads and closes the currently open object, if any.
// Callers serialize it with Read; repeated calls can repeat the active body
// close and return that body error.
func (r *vertexCombinedJSONLReadCloser) Close() error {
	r.closed = true
	if r.current != nil {
		return r.current.Close()
	}
	return nil
}

// NewVertexBatchHTTPClient retains a normalized base URL and borrowed HTTP
// client for job exchanges. A nil client uses DefaultBatchHTTPClient; a blank
// base URL selects Google endpoints during request construction.
func NewVertexBatchHTTPClient(baseURL string, client *http.Client) *VertexBatchHTTPClient {
	if client == nil {
		client = httpclient.DefaultBatchHTTPClient()
	}
	return &VertexBatchHTTPClient{baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"), client: client}
}

// CreateBatchPredictionJob submits req at its project and location with the
// supplied bearer token. Endpoint-only fields are omitted from JSON, the
// response body is closed here, and encoding or HTTP errors return to the
// caller.
func (c *VertexBatchHTTPClient) CreateBatchPredictionJob(ctx context.Context, accessToken string, req VertexCreateBatchPredictionJobRequest) (*VertexBatchPredictionJob, error) {
	endpoint, err := BuildVertexBatchPredictionJobsEndpoint(c.baseURL, req.ProjectID, req.Location)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+accessToken)
	return doVertexJSON[VertexBatchPredictionJob](c.client, httpReq)
}

// GetBatchPredictionJob fetches the supplied resource name with bearer
// authentication and decodes one job observation. It closes the HTTP body;
// callers select polling and interpret server state.
func (c *VertexBatchHTTPClient) GetBatchPredictionJob(ctx context.Context, accessToken string, name string) (*VertexBatchPredictionJob, error) {
	endpoint := c.vertexResourceURL(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	return doVertexJSON[VertexBatchPredictionJob](c.client, req)
}

// CancelBatchPredictionJob posts to the resource cancellation action and
// closes its response body. A successful HTTP status returns nil; callers poll
// the job to observe the resulting state.
func (c *VertexBatchHTTPClient) CancelBatchPredictionJob(ctx context.Context, accessToken string, name string) error {
	endpoint := c.vertexResourceURL(name) + ":cancel"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	return doVertexNoBody(c.client, req)
}

// vertexResourceURL appends a trimmed resource name under the selected API
// base. Names are supplied by job responses or callers; a blank base uses the
// common Google host.
func (c *VertexBatchHTTPClient) vertexResourceURL(name string) string {
	name = strings.TrimLeft(strings.TrimSpace(name), "/")
	if c.baseURL != "" {
		return c.baseURL + "/v1/" + name
	}
	return "https://aiplatform.googleapis.com/v1/" + name
}

// NewVertexGCSObjectStore retains a normalized GCS API base and borrowed HTTP
// client. Blank base uses storage.googleapis.com, while a nil client uses
// DefaultBatchHTTPClient with its shared transport settings.
func NewVertexGCSObjectStore(baseURL string, client *http.Client) *VertexGCSObjectStore {
	if client == nil {
		client = httpclient.DefaultBatchHTTPClient()
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "https://storage.googleapis.com"
	}
	return &VertexGCSObjectStore{baseURL: baseURL, client: client}
}

// UploadJSONL uploads r as application/jsonl to a validated GCS object path
// using bearer authentication. The HTTP client owns any request-body closing;
// this method closes the response and returns exchange errors.
func (s *VertexGCSObjectStore) UploadJSONL(ctx context.Context, accessToken string, uri string, r io.Reader) error {
	bucket, object, err := parseGCSURI(uri)
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/upload/storage/v1/b/%s/o?uploadType=media&name=%s", s.baseURL, url.PathEscape(bucket), url.QueryEscape(object))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/jsonl")
	return doVertexNoBody(s.client, req)
}

// ListJSONLObjects returns all listed object names ending in the
// case-sensitive .jsonl suffix under prefixURI. Pages retain server order, and
// any page failure returns an error instead of a partial list.
func (s *VertexGCSObjectStore) ListJSONLObjects(ctx context.Context, accessToken string, prefixURI string) ([]string, error) {
	return s.listObjects(ctx, accessToken, prefixURI, true)
}

// listObjects follows returned page tokens and builds GCS object URIs in
// response order. jsonlOnly filters names by the .jsonl suffix; failures
// discard the accumulated list and return the exchange error.
func (s *VertexGCSObjectStore) listObjects(ctx context.Context, accessToken string, prefixURI string, jsonlOnly bool) ([]string, error) {
	bucket, prefix, err := parseGCSURI(prefixURI)
	if err != nil {
		return nil, err
	}
	var objects []string
	pageToken := ""
	for {
		endpoint := fmt.Sprintf("%s/storage/v1/b/%s/o?prefix=%s", s.baseURL, url.PathEscape(bucket), url.QueryEscape(prefix))
		if pageToken != "" {
			endpoint += "&pageToken=" + url.QueryEscape(pageToken)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		var page struct {
			Items []struct { // current page object entries
				Name string `json:"name"` // listed GCS object name
			} `json:"items"`
			NextPageToken string `json:"nextPageToken"` // continuation token or empty completion
		}
		if err := doVertexDecodeJSON(s.client, req, &page); err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			if !jsonlOnly || strings.HasSuffix(item.Name, ".jsonl") {
				objects = append(objects, "gs://"+bucket+"/"+item.Name)
			}
		}
		if page.NextPageToken == "" {
			return objects, nil
		}
		// Follow the returned token before returning any accumulated names so
		// cleanup and combined downloads receive the complete traversed prefix
		// listing.
		pageToken = page.NextPageToken
	}
}

// OpenObject opens a GCS media download using the request context and bearer
// token. On success, callers own and close its body; absent content type
// selects application/jsonl, while failed responses are closed here.
func (s *VertexGCSObjectStore) OpenObject(ctx context.Context, accessToken string, uri string) (io.ReadCloser, string, error) {
	bucket, object, err := parseGCSURI(uri)
	if err != nil {
		return nil, "", err
	}
	endpoint := fmt.Sprintf("%s/storage/v1/b/%s/o/%s?alt=media", s.baseURL, url.PathEscape(bucket), url.PathEscape(object))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		return nil, "", readVertexAPIError(resp)
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/jsonl"
	}
	return resp.Body, contentType, nil
}

// DeleteObject removes one parsed GCS object with bearer authentication and
// closes the response body. Invalid URIs and non-success statuses return
// errors; a successful response returns nil.
func (s *VertexGCSObjectStore) DeleteObject(ctx context.Context, accessToken string, uri string) error {
	bucket, object, err := parseGCSURI(uri)
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/storage/v1/b/%s/o/%s", s.baseURL, url.PathEscape(bucket), url.PathEscape(object))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	return doVertexNoBody(s.client, req)
}

// DeletePrefix lists every object under prefixURI and deletes them in listing
// order. A listing failure begins no deletion; a delete failure stops the
// sequence while earlier successful removals remain applied.
//
// Cleanup covers all suffixes because task prefixes can contain both JSONL
// predictions and ancillary files. The initial list is one traversal, so
// objects created after listing may remain. Callers own the prefix, decide
// retries after partial cleanup, and coordinate deletion with downloads still
// using those objects.
func (s *VertexGCSObjectStore) DeletePrefix(ctx context.Context, accessToken string, prefixURI string) error {
	objects, err := s.listObjects(ctx, accessToken, prefixURI, false)
	if err != nil {
		return err
	}
	// Delete the completed listing in order and stop on the first failure;
	// callers can retry cleanup after the already removed objects remain absent.
	for _, object := range objects {
		if err := s.DeleteObject(ctx, accessToken, object); err != nil {
			return err
		}
	}
	return nil
}

// parseGCSURI splits a trimmed gs:// URI into bucket and nonblank object text.
// It validates the prefix and presence of both pieces, retains their internal
// spelling, and leaves GCS syntax enforcement to the service.
func parseGCSURI(uri string) (bucket, object string, err error) {
	uri = strings.TrimSpace(uri)
	if !strings.HasPrefix(uri, "gs://") {
		return "", "", fmt.Errorf("invalid gcs uri")
	}
	rest := strings.TrimPrefix(uri, "gs://")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("invalid gcs uri")
	}
	return parts[0], parts[1], nil
}

// Error formats the Vertex status and message, including a symbolic code when
// available. A nil receiver formats as <nil>, allowing diagnostic formatting
// of an absent typed error.
func (e *VertexAPIError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Code != "" {
		return fmt.Sprintf("vertex api error: status=%d code=%s message=%s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("vertex api error: status=%d message=%s", e.StatusCode, e.Message)
}

// doVertexJSON sends req, decodes one typed response, and returns its
// allocated value. The lower exchange closes the response body and propagates
// transport, status, and decode errors.
func doVertexJSON[T any](client *http.Client, req *http.Request) (*T, error) {
	var out T
	if err := doVertexDecodeJSON(client, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// doVertexDecodeJSON sends req and decodes one JSON value into out on a
// successful HTTP status. It closes the response body and returns a
// VertexAPIError for non-success responses.
func doVertexDecodeJSON(client *http.Client, req *http.Request, out any) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return readVertexAPIError(resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// doVertexNoBody sends req and closes its response body after checking status.
// Successful response content is discarded; a non-success status returns the
// bounded server error description.
func doVertexNoBody(client *http.Client, req *http.Request) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return readVertexAPIError(resp)
	}
	return nil
}

// readVertexAPIError selects a structured message and symbolic status from at
// most eight KiB of response text. Read errors are ignored, and malformed
// structured errors retain the collected raw prefix; the caller owns response
// closing.
func readVertexAPIError(resp *http.Response) error {
	// Keep at most eight KiB of diagnostic text. A read failure still supplies
	// its collected prefix, and malformed structured errors retain that raw text.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	message := string(body)
	code := ""
	var parsed struct {
		Error struct { // structured upstream error details
			Code    any    `json:"code"`    // numeric upstream error code
			Message string `json:"message"` // upstream error description text
			Status  string `json:"status"`  // upstream symbolic status text
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error.Message != "" {
		message = parsed.Error.Message
		code = parsed.Error.Status
	}
	return &VertexAPIError{StatusCode: resp.StatusCode, Code: code, Message: message}
}

// NewCombinedJSONLReadCloser creates a lazy sequential reader over the
// borrowed objects slice. Callers leave that slice unchanged, serialize reads
// and closing, use nonempty read buffers to retain separators, and close the
// reader to release its active object body.
func NewCombinedJSONLReadCloser(ctx context.Context, token string, objects []string, store VertexBatchObjectStore) io.ReadCloser {
	return &vertexCombinedJSONLReadCloser{ctx: ctx, accessToken: token, objects: objects, store: store}
}
