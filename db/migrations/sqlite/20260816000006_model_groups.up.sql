-- Create "model_groups" table
CREATE TABLE `model_groups` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `name` text NOT NULL,
  `slug` text NOT NULL,
  `user_id` integer NULL,
  `description` text NULL,
  `created_at` datetime NULL,
  `updated_at` datetime NULL
);
-- Create index "idx_model_groups_user_id" to table: "model_groups"
CREATE INDEX `idx_model_groups_user_id` ON `model_groups` (`user_id`);
-- Create index "idx_model_groups_slug" to table: "model_groups"
CREATE UNIQUE INDEX `idx_model_groups_slug` ON `model_groups` (`slug`);
