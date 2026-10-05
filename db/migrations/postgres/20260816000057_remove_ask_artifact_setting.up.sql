DELETE FROM "public"."default_model_settings" WHERE "purpose" = 'ask_artifact';
ALTER TABLE "public"."default_model_settings" DROP CONSTRAINT IF EXISTS "ck_default_model_settings_purpose_enum";
ALTER TABLE "public"."default_model_settings" ADD CONSTRAINT "ck_default_model_settings_purpose_enum"
  CHECK ("purpose" IN ('commit_messages', 'task_orchestrator', 'helper_worker'));
