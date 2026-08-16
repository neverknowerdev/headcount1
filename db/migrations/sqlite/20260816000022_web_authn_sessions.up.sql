-- Create "web_authn_sessions" table
CREATE TABLE `web_authn_sessions` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `user_id` integer NULL,
  `purpose` text NOT NULL,
  `data` text NOT NULL,
  `expires_at` datetime NOT NULL,
  `created_at` datetime NULL
);
-- Create index "idx_web_authn_sessions_expires_at" to table: "web_authn_sessions"
CREATE INDEX `idx_web_authn_sessions_expires_at` ON `web_authn_sessions` (`expires_at`);
-- Create index "idx_web_authn_sessions_user_id" to table: "web_authn_sessions"
CREATE INDEX `idx_web_authn_sessions_user_id` ON `web_authn_sessions` (`user_id`);
