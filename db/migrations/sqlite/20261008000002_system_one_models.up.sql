-- System One models (TypeSafe's Jev) are classifiers, not language models.
-- A provider may serve both kinds, so it keeps them in separate catalogs, and
-- a model group routes between models of one kind only.
ALTER TABLE `llm_providers` ADD COLUMN `system_one_models` text NOT NULL DEFAULT '';
ALTER TABLE `model_groups` ADD COLUMN `kind` text NOT NULL DEFAULT 'llm';
ALTER TABLE `model_groups` ADD COLUMN `__enum_guard_kind` integer NOT NULL DEFAULT 1
  CHECK (`kind` IN ('llm', 'system_one'));
