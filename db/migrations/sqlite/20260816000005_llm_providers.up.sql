-- Create "llm_providers" table
CREATE TABLE `llm_providers` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `name` text NOT NULL,
  `base_url` text NOT NULL,
  `api_key` text NOT NULL,
  `user_id` integer NULL,
  `provider_type` text NULL,
  `default_model` text NULL,
  `supported_models` text NULL,
  `builtin` numeric NOT NULL DEFAULT false,
  `enabled` numeric NOT NULL DEFAULT true,
  `preset_key` text NULL DEFAULT '',
  `provider_name` text NULL DEFAULT '',
  `slug` text NULL DEFAULT '',
  `created_at` datetime NULL,
  `updated_at` datetime NULL
);
-- Create index "idx_llm_providers_slug" to table: "llm_providers"
CREATE INDEX `idx_llm_providers_slug` ON `llm_providers` (`slug`);
-- Create index "idx_llm_providers_user_id" to table: "llm_providers"
CREATE INDEX `idx_llm_providers_user_id` ON `llm_providers` (`user_id`);
