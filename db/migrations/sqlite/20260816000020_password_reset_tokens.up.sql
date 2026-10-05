-- Create "password_reset_tokens" table
CREATE TABLE `password_reset_tokens` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `token_hash` text NOT NULL,
  `user_id` integer NOT NULL,
  `expires_at` datetime NOT NULL,
  `used_at` datetime NULL,
  `created_at` datetime NULL,
  CONSTRAINT `fk_password_reset_tokens_user` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_password_reset_tokens_user_id" to table: "password_reset_tokens"
CREATE INDEX `idx_password_reset_tokens_user_id` ON `password_reset_tokens` (`user_id`);
-- Create index "idx_password_reset_tokens_token_hash" to table: "password_reset_tokens"
CREATE UNIQUE INDEX `idx_password_reset_tokens_token_hash` ON `password_reset_tokens` (`token_hash`);
