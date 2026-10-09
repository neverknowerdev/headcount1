-- The usage ledger: one row per model call of any kind, including failed and
-- rejected ones. task_id/agent_id are plain references so usage history
-- outlives the rows it describes; the company owns the ledger.
CREATE TABLE `llm_calls` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `company_id` integer NOT NULL,
  `root_task_id` integer NULL,
  `task_id` integer NULL,
  `agent_id` integer NULL,
  `agent_name` text NOT NULL DEFAULT '',
  `tier` text NOT NULL,
  `purpose` text NOT NULL DEFAULT '',
  `phase` text NOT NULL DEFAULT '',
  `workflow_phase` text NOT NULL DEFAULT '',
  `provider_id` integer NULL,
  `provider_name` text NOT NULL DEFAULT '',
  `model` text NOT NULL DEFAULT '',
  `requested_model` text NOT NULL DEFAULT '',
  `step_id` integer NULL,
  `run_id` integer NULL,
  `log_seq` integer NULL,
  `prompt_tokens` integer NOT NULL DEFAULT 0,
  `completion_tokens` integer NOT NULL DEFAULT 0,
  `reasoning_tokens` integer NOT NULL DEFAULT 0,
  `cached_tokens` integer NOT NULL DEFAULT 0,
  `duration_ms` integer NOT NULL DEFAULT 0,
  `status` text NOT NULL DEFAULT 'ok',
  `error` text NOT NULL DEFAULT '',
  `created_at` datetime NULL,
  CONSTRAINT `fk_llm_calls_company` FOREIGN KEY (`company_id`) REFERENCES `companies` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
ALTER TABLE `llm_calls` ADD COLUMN `__enum_guard_tier` integer NOT NULL DEFAULT 1
  CHECK (`tier` IN ('smart', 'cheap', 'classifier', 'commit'));
ALTER TABLE `llm_calls` ADD COLUMN `__enum_guard_status` integer NOT NULL DEFAULT 1
  CHECK (`status` IN ('ok', 'error', 'rejected'));
CREATE INDEX `idx_llm_calls_company_created` ON `llm_calls` (`company_id`, `created_at`);
CREATE INDEX `idx_llm_calls_root_task_id` ON `llm_calls` (`root_task_id`);
CREATE INDEX `idx_llm_calls_task_id` ON `llm_calls` (`task_id`);
CREATE INDEX `idx_llm_calls_agent_id` ON `llm_calls` (`agent_id`);
CREATE INDEX `idx_llm_calls_model` ON `llm_calls` (`model`);
CREATE INDEX `idx_llm_calls_step_id` ON `llm_calls` (`step_id`);
CREATE INDEX `idx_llm_calls_run_id` ON `llm_calls` (`run_id`);
