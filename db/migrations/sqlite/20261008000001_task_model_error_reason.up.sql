-- model_error: a task whose model could not be called. It was never attempted,
-- so its parent does not re-plan around it; the problem goes to the human.
ALTER TABLE `tasks` DROP COLUMN `__enum_guard_result_reason`;
ALTER TABLE `tasks` ADD COLUMN `__enum_guard_result_reason` integer NOT NULL DEFAULT 1
  CHECK (`result_reason` IN ('', 'reported_failure', 'cannot_complete', 'run_error', 'budget_exhausted', 'prerequisite_failed', 'stopped', 'model_error'));
