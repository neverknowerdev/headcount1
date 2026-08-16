-- Create "users" table
CREATE TABLE `users` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `email` text NOT NULL,
  `is_admin` numeric NOT NULL DEFAULT false,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  `reenroll_token_hash` text NULL,
  `reenroll_expires_at` datetime NULL
);
-- Create index "idx_users_email" to table: "users"
CREATE UNIQUE INDEX `idx_users_email` ON `users` (`email`);
