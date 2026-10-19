-- 299_custom_video_resolution.sql expands recorded video resolution labels to
-- match configured pricing tiers. Existing usage and task charges remain intact;
-- new binaries retain custom labels when selecting and recording per-second prices.
SET LOCAL lock_timeout = '3s';

ALTER TABLE usage_logs ALTER COLUMN video_resolution TYPE VARCHAR(50);
COMMENT ON COLUMN usage_logs.video_resolution IS 'Normalized video resolution label used for per-second billing';
COMMENT ON COLUMN usage_logs.video_duration_seconds IS 'Billable video seconds from completion, submission, or the missing-duration default';
