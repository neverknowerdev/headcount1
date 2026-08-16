-- Create "provider_presets" table
CREATE TABLE `provider_presets` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `key` text NOT NULL,
  `name` text NOT NULL,
  `base_url` text NOT NULL,
  `provider_type` text NOT NULL,
  `created_at` datetime NULL,
  `updated_at` datetime NULL
);
-- Create index "idx_provider_presets_key" to table: "provider_presets"
CREATE UNIQUE INDEX `idx_provider_presets_key` ON `provider_presets` (`key`);
