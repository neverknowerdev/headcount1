-- Create "teams" table
CREATE TABLE `teams` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `name` text NOT NULL,
  `created_at` datetime NULL,
  `updated_at` datetime NULL
);
