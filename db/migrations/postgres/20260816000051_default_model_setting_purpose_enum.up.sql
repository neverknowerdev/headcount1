ALTER TABLE "public"."default_model_settings"
  ADD CONSTRAINT "ck_default_model_settings_purpose_enum" CHECK ("purpose" IN ('commit_messages', 'ask_artifact', 'task_orchestrator'));
