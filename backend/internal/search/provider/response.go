package provider

// Supplier response readers retain at most one MiB before JSON decoding or
// status reporting. Error status messages include a smaller two-hundred-byte
// prefix and a truncation marker, which bounds logged supplier text even when
// the response contains a large diagnostic body.

// These byte limits bound supplier response input and the diagnostic excerpts
// returned for non-success HTTP statuses.
const (
	maxResponseSize   = 1 << 20 // maximum decoded response bytes
	errorBodyTruncLen = 200     // retained error excerpt bytes
)

// truncateBody returns at most two hundred body bytes followed by a marker when
// truncated. Shorter bodies return as strings; the byte prefix may split a
// UTF-8 sequence, so this helper does not promise valid UTF-8.
func truncateBody(body []byte) string {
	if len(body) <= errorBodyTruncLen {
		return string(body)
	}
	// Keep the diagnostic byte prefix and mark omitted content; callers report
	// this excerpt after an unsuccessful supplier HTTP status.
	return string(body[:errorBodyTruncLen]) + "...(truncated)"
}
