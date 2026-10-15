package ops

import (
	"math"
	"time"
)

// health_score.go calculates dashboard health scores from traffic errors,
// latency, infrastructure measurements, and job heartbeats.
// Dashboard health scoring combines a selected traffic window with
// independently collected system samples and job heartbeats. An interval with
// no positive request or client-error counts receives one hundred immediately,
// so infrastructure failures alone do not lower an idle interval score. For
// traffic-bearing intervals, client and provider error fractions share one
// error input, while first-token P99 supplies a separate latency input.
//
// Missing optional measurements retain healthy component defaults. Database
// probe failure takes precedence over Redis failure; CPU, memory, and disk each
// have a fixed penalty curve. Heartbeat timestamps determine job failures,
// including a fifteen-minute successful-run freshness limit. These fixed
// formulas produce a bounded, rounded diagnostic integer independently of
// configurable dashboard warning and critical display thresholds.

// computeDashboardHealthScore returns the rounded dashboard diagnostic score
// from traffic and infrastructure observations. Nil overview returns zero. If
// all three request/SLA/client-error counts are nonpositive, the interval
// receives one hundred before optional infrastructure signals are inspected.
//
// For traffic-bearing intervals, computeBusinessHealth contributes seventy
// percent and computeInfraHealth contributes thirty percent. Missing
// measurements keep the defaults defined by those helpers. The combined value
// is clamped to 0-100 before rounding, and now supplies the reference time used
// to classify stale job heartbeats.
func computeDashboardHealthScore(now time.Time, overview *OpsDashboardOverview) int {
	if overview == nil {
		return 0
	}

	// The empty-traffic branch represents an idle interval as fully scored even
	// when its optional system sample records a failed probe.
	if overview.RequestCountSLA <= 0 && overview.RequestCountTotal <= 0 && overview.ErrorCountTotal <= 0 {
		return 100
	}

	businessHealth := computeBusinessHealth(overview)
	infraHealth := computeInfraHealth(now, overview)

	// The traffic window receives the larger weight, while the independent latest
	// system sample and job diagnostics supply the remaining contribution. Round
	// the combined value for integer display.
	score := businessHealth*0.7 + infraHealth*0.3
	return int(math.Round(clampFloat64(score, 0, 100)))
}

// computeBusinessHealth returns the traffic score from a nonnil overview,
// giving error rate and first-token latency equal weight. The greater of client
// ErrorRate and UpstreamErrorRate is converted from a fraction to a clamped
// percentage. Its score stays one hundred through one percent and falls
// linearly to zero at ten percent.
//
// TTFT.P99 uses milliseconds: one second retains one hundred and three seconds
// reaches zero, with a linear penalty between them. Missing TTFT keeps its
// healthy default. SLA and full request duration remain separate displayed
// observations and do not supply this formula. A NaN rate makes the combined
// rate incomparable and leaves its error component at one hundred.
func computeBusinessHealth(overview *OpsDashboardOverview) float64 {
	// Error-rate and TTFT cutoffs belong to this fixed scoring formula. Runtime
	// dashboard warning and critical thresholds are read through another endpoint
	// and do not alter these curves.
	errorScore := 100.0
	// Dashboard rate fields store fractions, so comparison with percentage cutoffs
	// first converts both rates to the 0-100 scale.
	errorPct := clampFloat64(overview.ErrorRate*100, 0, 100)
	upstreamPct := clampFloat64(overview.UpstreamErrorRate*100, 0, 100)
	// Client failures and provider-owned failures can overlap. Using their greater
	// percentage keeps a shared observation from being added twice to the error
	// input.
	combinedErrorPct := math.Max(errorPct, upstreamPct)
	if combinedErrorPct > 1.0 {
		if combinedErrorPct <= 10.0 {
			errorScore = (10.0 - combinedErrorPct) / 9.0 * 100
		} else {
			errorScore = 0
		}
	}

	// First-token P99 is measured in milliseconds. Full request duration is a
	// separate latency observation, so it cannot substitute for this scoring
	// input.
	ttftScore := 100.0
	if overview.TTFT.P99 != nil {
		p99 := float64(*overview.TTFT.P99)
		if p99 > 1000 {
			if p99 <= 3000 {
				ttftScore = (3000 - p99) / 2000 * 100
			} else {
				ttftScore = 0
			}
		}
	}

	// Latency retains half the traffic score even when TTFT is absent, because an
	// unmeasured first-token component keeps its healthy default.
	return errorScore*0.5 + ttftScore*0.5
}

// computeInfraHealth returns the storage, resource, and job score for a nonnil
// overview at now. Storage receives forty percent, resources thirty percent,
// and jobs thirty percent. A failed database probe sets storage to zero;
// otherwise a failed Redis probe gives fifty, while missing probes keep one
// hundred.
//
// CPU begins its linear penalty above eighty percent, memory and disk above
// eighty-five, after usage is clamped to 0-100. Their scores are averaged with
// healthy defaults for missing samples; a NaN percentage also keeps that
// resource's healthy default. Each nonnil heartbeat fails when an
// error has no success or is later than it, or when success is over fifteen
// minutes old. A heartbeat with neither result timestamp stays healthy.
func computeInfraHealth(now time.Time, overview *OpsDashboardOverview) float64 {
	// Storage probes share one score. A failed database probe wins even when Redis
	// also fails; the Redis penalty is selected only after that database-failure
	// check.
	storageScore := 100.0
	if overview.SystemMetrics != nil {
		if overview.SystemMetrics.DBOK != nil && !*overview.SystemMetrics.DBOK {
			storageScore = 0
		} else if overview.SystemMetrics.RedisOK != nil && !*overview.SystemMetrics.RedisOK {
			storageScore = 50
		}
	}

	// Resource usage comes from optional samples, and each absent resource keeps
	// one hundred. Averaging all three components includes those defaults
	// alongside the observed penalties.
	computeScore := 100.0
	if overview.SystemMetrics != nil {
		cpuScore := 100.0
		if overview.SystemMetrics.CPUUsagePercent != nil {
			cpuPct := clampFloat64(*overview.SystemMetrics.CPUUsagePercent, 0, 100)
			// The CPU penalty starts at eighty percent of the measured host or container
			// usage.
			if cpuPct > 80 {
				if cpuPct <= 100 {
					cpuScore = (100 - cpuPct) / 20 * 100
				} else {
					cpuScore = 0
				}
			}
		}

		memScore := 100.0
		if overview.SystemMetrics.MemoryUsagePercent != nil {
			memPct := clampFloat64(*overview.SystemMetrics.MemoryUsagePercent, 0, 100)
			// Memory uses the fixed eighty-five-percent onset for its independently
			// measured usage.
			if memPct > 85 {
				if memPct <= 100 {
					memScore = (100 - memPct) / 15 * 100
				} else {
					memScore = 0
				}
			}
		}

		diskScore := 100.0
		if overview.SystemMetrics.DiskUsagePercent != nil {
			diskPct := clampFloat64(*overview.SystemMetrics.DiskUsagePercent, 0, 100)
			// Disk uses the same fixed onset as memory for its separate usage sample.
			if diskPct > 85 {
				if diskPct <= 100 {
					diskScore = (100 - diskPct) / 15 * 100
				} else {
					diskScore = 0
				}
			}
		}

		computeScore = (cpuScore + memScore + diskScore) / 3
	}

	// Count each nonnil heartbeat once. Its result timestamps select failure;
	// absent result timestamps keep the job healthy, and an error at the same time
	// as success does not win.
	jobScore := 100.0
	failedJobs := 0
	totalJobs := 0
	for _, hb := range overview.JobHeartbeats {
		if hb == nil {
			continue
		}
		totalJobs++
		// The fixed fifteen-minute freshness limit applies to the recorded successful
		// run, independently of the execution schedule associated with that job.
		if hb.LastErrorAt != nil && (hb.LastSuccessAt == nil || hb.LastErrorAt.After(*hb.LastSuccessAt)) {
			failedJobs++
		} else if hb.LastSuccessAt != nil && now.Sub(*hb.LastSuccessAt) > 15*time.Minute {
			failedJobs++
		}
	}
	if totalJobs > 0 && failedJobs > 0 {
		jobScore = (1 - float64(failedJobs)/float64(totalJobs)) * 100
	}

	// The shared storage score carries more weight than either resource or job
	// diagnostics, whose samples can be missing independently.
	return storageScore*0.4 + computeScore*0.3 + jobScore*0.3
}

// clampFloat64 applies lower and upper comparisons to return v within ordered
// inclusive bounds. Callers supply min no greater than max; a NaN value passes
// through unchanged because neither comparison succeeds.
func clampFloat64(v float64, min float64, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
