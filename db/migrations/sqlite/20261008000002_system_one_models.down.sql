-- A group of System One models has no meaning without the kind, and a
-- language model slot cannot hold one: they go.
DELETE FROM `model_groups` WHERE `kind` = 'system_one';
ALTER TABLE `model_groups` DROP COLUMN `__enum_guard_kind`;
ALTER TABLE `model_groups` DROP COLUMN `kind`;
ALTER TABLE `llm_providers` DROP COLUMN `system_one_models`;
