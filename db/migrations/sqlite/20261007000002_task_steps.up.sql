-- The per-task journal: every smart prompt and answer, phase change, subtask
-- reference, human question and executor run, in order. Append-only.
CREATE TABLE `task_steps` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `task_id` integer NOT NULL,
  `root_task_id` integer NOT NULL DEFAULT 0,
  `kind` text NOT NULL,
  `phase` text NOT NULL DEFAULT '',
  `agent_id` integer NULL,
  `run_id` integer NULL,
  `ref_task_id` integer NULL,
  `comment_id` integer NULL,
  `tool_name` text NOT NULL DEFAULT '',
  `tool_args` text NOT NULL DEFAULT '',
  `prompt` text NOT NULL DEFAULT '',
  `response` text NOT NULL DEFAULT '',
  `result` text NOT NULL DEFAULT '',
  `error` text NOT NULL DEFAULT '',
  `created_at` datetime NULL,
  CONSTRAINT `fk_task_steps_task` FOREIGN KEY (`task_id`) REFERENCES `tasks` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
ALTER TABLE `task_steps` ADD COLUMN `__enum_guard_kind` integer NOT NULL DEFAULT 1
  CHECK (`kind` IN ('workflow_started', 'phase_entered', 'smart_call', 'smart_error', 'subtask_finished', 'human_question', 'human_answer', 'run_started', 'run_finished', 'checkpoint', 'waiting', 'resumed', 'review_round', 'stopped', 'rerun', 'finished', 'note'));
CREATE INDEX `idx_task_steps_task_id` ON `task_steps` (`task_id`, `id`);
CREATE INDEX `idx_task_steps_root_task_id` ON `task_steps` (`root_task_id`);
