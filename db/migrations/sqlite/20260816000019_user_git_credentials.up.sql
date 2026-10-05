-- Create "user_git_credentials" table
CREATE TABLE `user_git_credentials` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `user_id` integer NOT NULL,
  `ssh_private_key` text NULL,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  CONSTRAINT `fk_user_git_credentials_user` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_user_git_credentials_user_id" to table: "user_git_credentials"
CREATE UNIQUE INDEX `idx_user_git_credentials_user_id` ON `user_git_credentials` (`user_id`);
