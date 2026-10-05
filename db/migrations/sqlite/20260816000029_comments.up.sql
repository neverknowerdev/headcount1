-- Create "comments" table
CREATE TABLE `comments` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `task_id` integer NOT NULL,
  `author_type` text NOT NULL,
  `author_id` integer NULL,
  `content` text NOT NULL,
  `comment_type` text NULL DEFAULT '',
  `run_id` integer NULL,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  CONSTRAINT `fk_comments_task` FOREIGN KEY (`task_id`) REFERENCES `tasks` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
