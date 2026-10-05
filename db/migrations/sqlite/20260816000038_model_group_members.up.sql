-- Create "model_group_members" table
CREATE TABLE `model_group_members` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `group_id` integer NOT NULL,
  `provider_id` integer NOT NULL,
  `model` text NULL,
  `all_models` numeric NOT NULL DEFAULT false,
  `is_free` numeric NOT NULL DEFAULT false,
  `priority` integer NOT NULL DEFAULT 0,
  `created_at` datetime NULL,
  CONSTRAINT `fk_model_groups_members` FOREIGN KEY (`group_id`) REFERENCES `model_groups` (`id`) ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT `fk_model_group_members_provider` FOREIGN KEY (`provider_id`) REFERENCES `llm_providers` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_model_group_members_group_id" to table: "model_group_members"
CREATE INDEX `idx_model_group_members_group_id` ON `model_group_members` (`group_id`);
