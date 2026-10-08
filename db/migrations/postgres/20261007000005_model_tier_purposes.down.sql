-- The DELETE is irreversible: a user's tier model choices cannot be
-- reconstructed without a backup. This restores the schema domain only.
DELETE FROM "public"."default_model_settings" WHERE "purpose" IN ('smart', 'cheap', 'classifier');
ALTER TABLE "public"."default_model_settings" DROP CONSTRAINT IF EXISTS "ck_default_model_settings_purpose_enum";
ALTER TABLE "public"."default_model_settings" ADD CONSTRAINT "ck_default_model_settings_purpose_enum"
  CHECK ("purpose" IN ('commit_messages', 'task_orchestrator', 'helper_worker'));
