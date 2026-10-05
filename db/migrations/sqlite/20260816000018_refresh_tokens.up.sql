-- Create "refresh_tokens" table
CREATE TABLE `refresh_tokens` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `family_id` text NOT NULL,
  `token_hash` text NOT NULL,
  `user_id` integer NOT NULL,
  `expires_at` datetime NOT NULL,
  `absolute_expires_at` datetime NOT NULL,
  `used_at` datetime NULL,
  `revoked_at` datetime NULL,
  `created_at` datetime NULL,
  CONSTRAINT `fk_refresh_tokens_user` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_refresh_tokens_user_id" to table: "refresh_tokens"
CREATE INDEX `idx_refresh_tokens_user_id` ON `refresh_tokens` (`user_id`);
-- Create index "idx_refresh_tokens_token_hash" to table: "refresh_tokens"
CREATE UNIQUE INDEX `idx_refresh_tokens_token_hash` ON `refresh_tokens` (`token_hash`);
-- Create index "idx_refresh_tokens_family_id" to table: "refresh_tokens"
CREATE INDEX `idx_refresh_tokens_family_id` ON `refresh_tokens` (`family_id`);
