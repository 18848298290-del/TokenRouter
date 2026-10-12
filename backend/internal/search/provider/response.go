package provider

const (
	maxResponseSize   = 1 << 20 // 1 MiB.
	errorBodyTruncLen = 200
)

// truncateBody limits error response excerpts to 200 bytes before appending a truncation marker.
func truncateBody(body []byte) string {
	if len(body) <= errorBodyTruncLen {
		return string(body)
	}
	return string(body[:errorBodyTruncLen]) + "...(truncated)"
}
