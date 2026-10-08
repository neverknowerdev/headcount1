-- Model tiers: smart (decides), cheap (executes) and classifier (gates). The
-- orchestrator-era purposes stay valid until the execution clean slate.
ALTER TABLE "public"."default_model_settings" DROP CONSTRAINT IF EXISTS "ck_default_model_settings_purpose_enum";
ALTER TABLE "public"."default_model_settings" ADD CONSTRAINT "ck_default_model_settings_purpose_enum"
  CHECK ("purpose" IN ('commit_messages', 'task_orchestrator', 'helper_worker', 'smart', 'cheap', 'classifier'));
