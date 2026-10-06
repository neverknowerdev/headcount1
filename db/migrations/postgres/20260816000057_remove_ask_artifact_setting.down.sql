-- The DELETE in the up migration is irreversible: user-specific model settings
-- cannot be reconstructed without a backup. This restores the schema domain
-- only; automatic deployment rollback must refuse this migration when rows
-- need to be restored.
ALTER TABLE "public"."default_model_settings"
  DROP CONSTRAINT IF EXISTS "ck_default_model_settings_purpose_enum";
ALTER TABLE "public"."default_model_settings"
  ADD CONSTRAINT "ck_default_model_settings_purpose_enum"
  CHECK ("purpose" IN ('commit_messages', 'ask_artifact', 'task_orchestrator', 'helper_worker'));
