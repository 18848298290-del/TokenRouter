package postgres

// provider_quota_expressions.go defines provider quota updates, expiry checks,
// and reset scheduling. The shared update records total usage together with
// each enabled period so a charge updates all of its counters atomically.
// Daily, weekly, and monthly periods roll from their recorded starts or reset
// at configured local calendar points. Expiry expressions treat only
// fixed mode as calendar scheduling; other or absent modes use the rolling
// duration. Missing start/reset timestamps use the epoch, making an enabled
// period eligible for initialization.
//
// After a fixed reset, the next timestamp is selected from the current
// configured local date and returned as UTC text. A provider inactive across
// several periods therefore advances to the next future point directly. These
// expressions cast stored JSON text and use supplied time-zone names, so
// malformed values can fail the owning SQL statement; callers validate
// configuration before persistence.

const (
	// providerQuotaIncrementSQL updates total and enabled periodic usage for an
	// undeleted provider. Parameter $1 is the increment amount and $2 is the
	// provider ID. Callers append their RETURNING columns and handle the result,
	// transaction completion, and any scheduler events in their owning paths.
	providerQuotaIncrementSQL = `UPDATE providers SET extra = (
			COALESCE(extra, '{}'::jsonb)
			-- Total usage includes every increment regardless of configured limits.
			|| jsonb_build_object('quota_used', COALESCE((extra->>'quota_used')::numeric, 0) + $1)
			-- A positive daily limit enables its counter and window metadata.
			|| CASE WHEN COALESCE((extra->>'quota_daily_limit')::numeric, 0) > 0 THEN
				jsonb_build_object(
					'quota_daily_used',
					CASE WHEN ` + DailyExpiredExpr + `
					THEN $1
					ELSE COALESCE((extra->>'quota_daily_used')::numeric, 0) + $1 END,
					'quota_daily_start',
					CASE WHEN ` + DailyExpiredExpr + `
					THEN ` + NowUTC + `
					ELSE COALESCE(extra->>'quota_daily_start', ` + NowUTC + `) END
				)
				-- Advance expired fixed windows so later increments use the new period.
				|| CASE WHEN ` + DailyExpiredExpr + ` AND ` + NextDailyResetAtExpr + ` IS NOT NULL
				   THEN jsonb_build_object('quota_daily_reset_at', ` + NextDailyResetAtExpr + `)
				   ELSE '{}'::jsonb END
			ELSE '{}'::jsonb END
			-- A positive weekly limit enables its counter and window metadata.
			|| CASE WHEN COALESCE((extra->>'quota_weekly_limit')::numeric, 0) > 0 THEN
				jsonb_build_object(
					'quota_weekly_used',
					CASE WHEN ` + WeeklyExpiredExpr + `
					THEN $1
					ELSE COALESCE((extra->>'quota_weekly_used')::numeric, 0) + $1 END,
					'quota_weekly_start',
					CASE WHEN ` + WeeklyExpiredExpr + `
					THEN ` + NowUTC + `
					ELSE COALESCE(extra->>'quota_weekly_start', ` + NowUTC + `) END
				)
				-- Advance expired fixed windows so later increments use the new period.
				|| CASE WHEN ` + WeeklyExpiredExpr + ` AND ` + NextWeeklyResetAtExpr + ` IS NOT NULL
				   THEN jsonb_build_object('quota_weekly_reset_at', ` + NextWeeklyResetAtExpr + `)
				   ELSE '{}'::jsonb END
			ELSE '{}'::jsonb END
			-- Monthly counters share the same update so accounting cannot commit
			-- total consumption while losing the corresponding monthly charge.
			|| CASE WHEN COALESCE((extra->>'quota_monthly_limit')::numeric, 0) > 0 THEN
				jsonb_build_object(
					'quota_monthly_used',
					CASE WHEN ` + MonthlyExpiredExpr + `
					THEN $1
					ELSE COALESCE((extra->>'quota_monthly_used')::numeric, 0) + $1 END,
					'quota_monthly_start',
					CASE WHEN ` + MonthlyExpiredExpr + `
					THEN ` + NowUTC + `
					ELSE COALESCE(extra->>'quota_monthly_start', ` + NowUTC + `) END
				)
				-- Expired fixed windows advance their reset deadline with the counter
				-- so the next increment stays in the newly initialized monthly period.
				|| CASE WHEN ` + MonthlyExpiredExpr + ` AND ` + NextMonthlyResetAtExpr + ` IS NOT NULL
				   THEN jsonb_build_object('quota_monthly_reset_at', ` + NextMonthlyResetAtExpr + `)
				   ELSE '{}'::jsonb END
			ELSE '{}'::jsonb END
		), updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL`

	// NowUTC formats PostgreSQL transaction time as UTC timestamp text for
	// provider JSON quota starts, retaining microsecond precision in the stored
	// representation.
	NowUTC = `to_char(NOW() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')`

	// DailyExpiredExpr selects whether the provider daily period is due at
	// transaction time. Fixed mode compares its saved reset timestamp; other
	// modes compare start plus twenty-four hours, and absent timestamps select
	// the epoch for initialization.
	DailyExpiredExpr = `(
	CASE WHEN COALESCE(extra->>'quota_daily_reset_mode', 'rolling') = 'fixed'
	THEN NOW() >= COALESCE((extra->>'quota_daily_reset_at')::timestamptz, '1970-01-01'::timestamptz)
	ELSE COALESCE((extra->>'quota_daily_start')::timestamptz, '1970-01-01'::timestamptz)
		+ '24 hours'::interval <= NOW()
	END
)`

	// WeeklyExpiredExpr selects whether the provider weekly period is due at
	// transaction time. Fixed mode compares its saved reset timestamp; other
	// modes compare start plus one hundred sixty-eight hours, and absent
	// timestamps select the epoch.
	WeeklyExpiredExpr = `(
	CASE WHEN COALESCE(extra->>'quota_weekly_reset_mode', 'rolling') = 'fixed'
	THEN NOW() >= COALESCE((extra->>'quota_weekly_reset_at')::timestamptz, '1970-01-01'::timestamptz)
	ELSE COALESCE((extra->>'quota_weekly_start')::timestamptz, '1970-01-01'::timestamptz)
		+ '168 hours'::interval <= NOW()
	END
)`

	// MonthlyExpiredExpr selects whether the provider monthly period is due at
	// transaction time. Fixed mode compares its saved reset timestamp; other
	// modes compare start plus seven hundred twenty hours, and absent
	// timestamps select the epoch.
	MonthlyExpiredExpr = `(
	CASE WHEN COALESCE(extra->>'quota_monthly_reset_mode', 'rolling') = 'fixed'
	THEN NOW() >= COALESCE((extra->>'quota_monthly_reset_at')::timestamptz, '1970-01-01'::timestamptz)
	ELSE COALESCE((extra->>'quota_monthly_start')::timestamptz, '1970-01-01'::timestamptz)
		+ '720 hours'::interval <= NOW()
	END
)`

	// NextDailyResetAtExpr formats the next future fixed daily reset in UTC
	// seconds, or returns SQL NULL for other modes. It uses the configured local
	// day/hour, advancing to tomorrow when transaction time has reached today's
	// point. Absent timezone and hour select UTC and zero; supplied invalid
	// values can fail SQL evaluation.
	NextDailyResetAtExpr = `(
	CASE WHEN COALESCE(extra->>'quota_daily_reset_mode', 'rolling') = 'fixed'
	THEN to_char((
		-- Compute today's reset point in the configured timezone, then pick next future one
		CASE WHEN NOW() >= (
			date_trunc('day', NOW() AT TIME ZONE COALESCE(extra->>'quota_reset_timezone', 'UTC'))
			+ (COALESCE((extra->>'quota_daily_reset_hour')::int, 0) || ' hours')::interval
		) AT TIME ZONE COALESCE(extra->>'quota_reset_timezone', 'UTC')
		-- NOW() is at or past today's reset point → next reset is tomorrow
		THEN (
			date_trunc('day', NOW() AT TIME ZONE COALESCE(extra->>'quota_reset_timezone', 'UTC'))
			+ (COALESCE((extra->>'quota_daily_reset_hour')::int, 0) || ' hours')::interval
			+ '1 day'::interval
		) AT TIME ZONE COALESCE(extra->>'quota_reset_timezone', 'UTC')
		-- NOW() is before today's reset point → next reset is today
		ELSE (
			date_trunc('day', NOW() AT TIME ZONE COALESCE(extra->>'quota_reset_timezone', 'UTC'))
			+ (COALESCE((extra->>'quota_daily_reset_hour')::int, 0) || ' hours')::interval
		) AT TIME ZONE COALESCE(extra->>'quota_reset_timezone', 'UTC')
		END
	) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
	ELSE NULL END
)`

	// NextWeeklyResetAtExpr formats the next future fixed weekly reset in UTC
	// seconds, or returns SQL NULL for other modes. It advances by PostgreSQL
	// weekday difference and, on the target weekday at/past its hour, by seven
	// calendar days. Absent day/hour/zone select Monday, zero and UTC; supplied
	// invalid cast/zone text can fail SQL evaluation.
	NextWeeklyResetAtExpr = `(
	CASE WHEN COALESCE(extra->>'quota_weekly_reset_mode', 'rolling') = 'fixed'
	THEN to_char((
		-- Compute this week's reset point in the configured timezone
		-- Step 1: get today's date at reset hour in configured tz
		-- Step 2: compute days forward to target weekday
		-- Step 3: if same day but past reset hour, advance 7 days
		CASE
		WHEN (
			-- days_forward = (target_day - current_day + 7) % 7
			(COALESCE((extra->>'quota_weekly_reset_day')::int, 1)
			 - EXTRACT(DOW FROM NOW() AT TIME ZONE COALESCE(extra->>'quota_reset_timezone', 'UTC'))::int
			 + 7) % 7
		) = 0 AND NOW() >= (
			date_trunc('day', NOW() AT TIME ZONE COALESCE(extra->>'quota_reset_timezone', 'UTC'))
			+ (COALESCE((extra->>'quota_weekly_reset_hour')::int, 0) || ' hours')::interval
		) AT TIME ZONE COALESCE(extra->>'quota_reset_timezone', 'UTC')
		-- Same weekday and past reset hour → next week
		THEN (
			date_trunc('day', NOW() AT TIME ZONE COALESCE(extra->>'quota_reset_timezone', 'UTC'))
			+ (COALESCE((extra->>'quota_weekly_reset_hour')::int, 0) || ' hours')::interval
			+ '7 days'::interval
		) AT TIME ZONE COALESCE(extra->>'quota_reset_timezone', 'UTC')
		ELSE (
			-- Advance to target weekday this week (or next if days_forward > 0)
			date_trunc('day', NOW() AT TIME ZONE COALESCE(extra->>'quota_reset_timezone', 'UTC'))
			+ (COALESCE((extra->>'quota_weekly_reset_hour')::int, 0) || ' hours')::interval
			+ ((
				(COALESCE((extra->>'quota_weekly_reset_day')::int, 1)
				 - EXTRACT(DOW FROM NOW() AT TIME ZONE COALESCE(extra->>'quota_reset_timezone', 'UTC'))::int
				 + 7) % 7
			) || ' days')::interval
		) AT TIME ZONE COALESCE(extra->>'quota_reset_timezone', 'UTC')
		END
	) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
	ELSE NULL END
)`

	// NextMonthlyResetAtExpr returns the next fixed monthly reset as UTC text.
	// The configured day is clamped independently in each target month so a
	// January thirty-first schedule resets in February and returns to March
	// thirty-first. Inactive schedules return SQL NULL.
	NextMonthlyResetAtExpr = `(
	CASE WHEN COALESCE(extra->>'quota_monthly_reset_mode', 'rolling') = 'fixed'
	THEN (
		SELECT to_char(
			MIN(reset_at) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'
		)
		FROM (
			SELECT (
				month_start
				+ (LEAST(COALESCE((extra->>'quota_monthly_reset_day')::int, 1),
					EXTRACT(DAY FROM month_start + interval '1 month - 1 day')::int) - 1) * interval '1 day'
				+ COALESCE((extra->>'quota_monthly_reset_hour')::int, 0) * interval '1 hour'
			) AT TIME ZONE COALESCE(extra->>'quota_reset_timezone', 'UTC') AS reset_at
			FROM (
				SELECT date_trunc('month', NOW() AT TIME ZONE COALESCE(extra->>'quota_reset_timezone', 'UTC'))
					+ month_offset * interval '1 month' AS month_start
				FROM generate_series(0, 1) AS month_offset
			) months
		) resets
		WHERE reset_at > NOW()
	)
	ELSE NULL END
)`
)
