-- Decisions recorded by smart steps and executor checkpoints. The hierarchy
-- follows the task tree and, within a task, parent_decision_id.
CREATE TABLE `decisions` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `task_id` integer NOT NULL,
  `root_task_id` integer NOT NULL DEFAULT 0,
  `parent_decision_id` integer NULL,
  `step_id` integer NULL,
  `run_id` integer NULL,
  `log_seq` integer NULL,
  `phase` text NOT NULL DEFAULT '',
  `agent_id` integer NULL,
  `kind` text NOT NULL DEFAULT 'decision',
  `title` text NOT NULL,
  `decision` text NOT NULL DEFAULT '',
  `rationale` text NOT NULL DEFAULT '',
  `alternatives` text NOT NULL DEFAULT '',
  `supersedes_id` integer NULL,
  `created_at` datetime NULL,
  CONSTRAINT `fk_decisions_task` FOREIGN KEY (`task_id`) REFERENCES `tasks` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
ALTER TABLE `decisions` ADD COLUMN `__enum_guard_kind` integer NOT NULL DEFAULT 1
  CHECK (`kind` IN ('decision', 'assumption', 'dead_end'));
CREATE INDEX `idx_decisions_task_id` ON `decisions` (`task_id`, `id`);
CREATE INDEX `idx_decisions_root_task_id` ON `decisions` (`root_task_id`);
