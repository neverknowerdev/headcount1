-- Create "attachments" table
CREATE TABLE `attachments` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `task_id` integer NOT NULL,
  `comment_id` integer NULL,
  `filename` text NOT NULL,
  `file_path` text NOT NULL,
  `mime_type` text NULL,
  `created_at` datetime NULL,
  CONSTRAINT `fk_attachments_comment` FOREIGN KEY (`comment_id`) REFERENCES `comments` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `fk_attachments_task` FOREIGN KEY (`task_id`) REFERENCES `tasks` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
