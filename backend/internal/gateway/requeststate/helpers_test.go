package requeststate

// benchmarkIntSink retains parsed lengths, field counts, and patched sizes so
// each benchmark iteration leaves an observable result for comparison.
var benchmarkIntSink int

// benchmarkBodySizes returns named partial-body targets from four through
// thirty-two MiB for request benchmarks. Builders can exceed those targets when
// completing messages and JSON framing, so benchmarks report each constructed
// body's actual length.
func benchmarkBodySizes() []struct {
	name  string // benchmark size case label
	bytes int    // partial body target bytes
} {
	return []struct {
		name  string // benchmark size case label
		bytes int    // partial body target bytes
	}{
		{name: "4MB", bytes: 4 << 20},
		{name: "8MB", bytes: 8 << 20},
		{name: "16MB", bytes: 16 << 20},
		{name: "32MB", bytes: 32 << 20},
	}
}
