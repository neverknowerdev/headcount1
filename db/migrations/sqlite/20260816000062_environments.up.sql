CREATE TABLE `environments` (
  `id` INTEGER PRIMARY KEY AUTOINCREMENT,
  `company_id` integer NOT NULL,
  `project_id` integer NULL,
  `name` text NOT NULL,
  `is_default` numeric NOT NULL DEFAULT false,
  `builtin` numeric NOT NULL DEFAULT false,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  CONSTRAINT `fk_environments_company` FOREIGN KEY (`company_id`) REFERENCES `companies` (`id`) ON DELETE CASCADE,
  CONSTRAINT `fk_environments_project` FOREIGN KEY (`project_id`) REFERENCES `projects` (`id`) ON DELETE CASCADE
);
CREATE INDEX `idx_environments_project_id` ON `environments` (`project_id`);
CREATE UNIQUE INDEX `idx_env_scope_name` ON `environments` (`company_id`, `project_id`, `name`);
CREATE UNIQUE INDEX `idx_env_platform_name` ON `environments` (`company_id`, `name`) WHERE project_id IS NULL;

CREATE TABLE `environment_secrets` (
  `id` INTEGER PRIMARY KEY AUTOINCREMENT,
  `environment_id` integer NOT NULL,
  `name` text NOT NULL,
  `kind` text NOT NULL DEFAULT 'secret',
  `value` text NULL,
  `user_id` integer NULL,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  CONSTRAINT `fk_environment_secrets_environment` FOREIGN KEY (`environment_id`) REFERENCES `environments` (`id`) ON DELETE CASCADE
);
CREATE INDEX `idx_environment_secrets_user_id` ON `environment_secrets` (`user_id`);
CREATE UNIQUE INDEX `idx_envsecret_env_name` ON `environment_secrets` (`environment_id`, `name`);

CREATE TABLE `environment_connectors` (
  `id` INTEGER PRIMARY KEY AUTOINCREMENT,
  `environment_id` integer NOT NULL,
  `provider` text NOT NULL,
  `target_project` text NOT NULL,
  `target_environment` text NOT NULL,
  `team_id` text NULL,
  `api_token` text NULL,
  `user_id` integer NULL,
  `last_sync_at` datetime NULL,
  `last_sync_status` text NULL,
  `last_sync_error` text NULL,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  CONSTRAINT `fk_environment_connectors_environment` FOREIGN KEY (`environment_id`) REFERENCES `environments` (`id`) ON DELETE CASCADE
);
CREATE INDEX `idx_environment_connectors_environment_id` ON `environment_connectors` (`environment_id`);
CREATE INDEX `idx_environment_connectors_user_id` ON `environment_connectors` (`user_id`);
