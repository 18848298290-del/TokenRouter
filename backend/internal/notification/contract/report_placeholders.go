package contract

// Operations reports can expose summary metrics individually in administrator
// templates. The shared placeholder names let report producers provide those
// values while notification validation and previews recognize the same names
// without decoding a report's rendered HTML.

// notificationEmailOpsSummaryPlaceholders names the independently selectable
// report summary values shared by report producers, template validation, and previews.
var notificationEmailOpsSummaryPlaceholders = []string{
	"report_summary_display",
	"report_total_requests",
	"report_success_count",
	"report_sla_error_count",
	"report_business_limited_count",
	"report_sla",
	"report_error_rate",
	"report_upstream_error_rate",
	"report_upstream_error_count_excl_429_529",
	"report_upstream_429_count",
	"report_upstream_529_count",
	"report_latency_p50",
	"report_latency_p99",
	"report_ttft_p50",
	"report_ttft_p99",
	"report_tokens",
	"report_qps_current",
	"report_qps_peak",
	"report_qps_avg",
	"report_tps_current",
	"report_tps_peak",
	"report_tps_avg",
}

// SummaryPlaceholders returns a separately allocated list of report summary variable names.
// Callers can append or reorder the returned string slice while the shared
// definition remains available for subsequent template validation and previews.
func SummaryPlaceholders() []string {
	return append([]string(nil), notificationEmailOpsSummaryPlaceholders...)
}
