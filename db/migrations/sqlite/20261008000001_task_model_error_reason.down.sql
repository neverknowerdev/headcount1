-- A model failure was a crashed session before it had a reason of its own.
UPDATE `tasks` SET `result_reason` = 'run_error' WHERE `result_reason` = 'model_error';
ALTER TABLE `tasks` DROP COLUMN `__enum_guard_result_reason`;
ALTER TABLE `tasks` ADD COLUMN `__enum_guard_result_reason` integer NOT NULL DEFAULT 1
  CHECK (`result_reason` IN ('', 'reported_failure', 'cannot_complete', 'run_error', 'budget_exhausted', 'prerequisite_failed', 'stopped'));
