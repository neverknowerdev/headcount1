-- Create "default_model_settings" table
CREATE TABLE `default_model_settings` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `purpose` text NOT NULL,
  `user_id` integer NULL,
  `provider_id` integer NULL,
  `model` text NULL,
  `model_group_id` integer NULL,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  CONSTRAINT `fk_default_model_settings_provider` FOREIGN KEY (`provider_id`) REFERENCES `llm_providers` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT `fk_default_model_settings_model_group` FOREIGN KEY (`model_group_id`) REFERENCES `model_groups` (`id`) ON UPDATE NO ACTION ON DELETE SET NULL
);
-- Create index "idx_dms_user_purpose" to table: "default_model_settings"
CREATE UNIQUE INDEX `idx_dms_user_purpose` ON `default_model_settings` (`purpose`, `user_id`);
