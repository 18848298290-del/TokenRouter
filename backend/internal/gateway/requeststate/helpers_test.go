package requeststate

// benchmarkIntSink retains counts from parsing and field-view benchmarks.
var benchmarkIntSink int

// benchmarkBodySizes defines target input sizes for parsing and field-reading benchmarks.
func benchmarkBodySizes() []struct {
	name  string
	bytes int
} {
	return []struct {
		name  string
		bytes int
	}{
		{name: "4MB", bytes: 4 << 20},
		{name: "8MB", bytes: 8 << 20},
		{name: "16MB", bytes: 16 << 20},
		{name: "32MB", bytes: 32 << 20},
	}
}
