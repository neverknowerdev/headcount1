-- Down migrations are retained for operators and are intentionally not embedded by the runtime.
DROP INDEX IF EXISTS `idx_run_events_reply_unique`;
DROP INDEX IF EXISTS `idx_run_events_dedupe_nonempty`;
DROP INDEX IF EXISTS `idx_run_events_reply_to`;
DROP INDEX IF EXISTS `idx_run_events_target_pending`;
