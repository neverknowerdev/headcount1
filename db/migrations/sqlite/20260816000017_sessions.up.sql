-- Create "sessions" table
CREATE TABLE `sessions` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `token_hash` text NOT NULL,
  `user_id` integer NOT NULL,
  `expires_at` datetime NOT NULL,
  `absolute_expires_at` datetime NULL,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  CONSTRAINT `fk_sessions_user` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_sessions_user_id" to table: "sessions"
CREATE INDEX `idx_sessions_user_id` ON `sessions` (`user_id`);
-- Create index "idx_sessions_token_hash" to table: "sessions"
CREATE UNIQUE INDEX `idx_sessions_token_hash` ON `sessions` (`token_hash`);
