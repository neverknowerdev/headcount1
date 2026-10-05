-- Create "task_relations" table
CREATE TABLE `task_relations` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `company_id` integer NOT NULL,
  `source_task_id` integer NOT NULL,
  `target_task_id` integer NOT NULL,
  `kind` text NOT NULL,
  `created_at` datetime NULL,
  CONSTRAINT `fk_task_relations_target_task` FOREIGN KEY (`target_task_id`) REFERENCES `tasks` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `fk_task_relations_source_task` FOREIGN KEY (`source_task_id`) REFERENCES `tasks` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_task_relations_kind" to table: "task_relations"
CREATE INDEX `idx_task_relations_kind` ON `task_relations` (`kind`);
-- Create index "idx_task_relations_target_task_id" to table: "task_relations"
CREATE INDEX `idx_task_relations_target_task_id` ON `task_relations` (`target_task_id`);
-- Create index "idx_task_relations_unique" to table: "task_relations"
CREATE UNIQUE INDEX `idx_task_relations_unique` ON `task_relations` (`source_task_id`, `target_task_id`, `kind`);
-- Create index "idx_task_relations_source_task_id" to table: "task_relations"
CREATE INDEX `idx_task_relations_source_task_id` ON `task_relations` (`source_task_id`);
-- Create index "idx_task_relations_company_id" to table: "task_relations"
CREATE INDEX `idx_task_relations_company_id` ON `task_relations` (`company_id`);
