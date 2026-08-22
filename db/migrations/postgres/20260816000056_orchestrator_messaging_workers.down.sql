DROP INDEX IF EXISTS "public"."idx_run_events_reply_unique";
DROP INDEX IF EXISTS "public"."idx_run_events_dedupe_nonempty";
DROP INDEX IF EXISTS "public"."idx_run_events_reply_to";
DROP INDEX IF EXISTS "public"."idx_run_events_target_pending";

ALTER TABLE "public"."default_model_settings"
  DROP CONSTRAINT IF EXISTS "ck_default_model_settings_purpose_enum";
ALTER TABLE "public"."default_model_settings"
  ADD CONSTRAINT "ck_default_model_settings_purpose_enum"
  CHECK ("purpose" IN ('commit_messages', 'ask_artifact', 'task_orchestrator'));

ALTER TABLE "public"."run_events"
  DROP CONSTRAINT IF EXISTS "ck_run_events_event_type_enum";
ALTER TABLE "public"."run_events"
  ADD CONSTRAINT "ck_run_events_event_type_enum"
  CHECK ("event_type" IN ('run_status', 'status_report', 'status_report_request', 'worker_question'));
ALTER TABLE "public"."run_events"
  DROP COLUMN IF EXISTS "reply_to_event_id";
ALTER TABLE "public"."run_events"
  DROP COLUMN IF EXISTS "target_run_id";
ALTER TABLE "public"."run_events"
  DROP COLUMN IF EXISTS "source_run_id";

ALTER TABLE "public"."runs"
  DROP CONSTRAINT IF EXISTS "ck_runs_kind_enum";
ALTER TABLE "public"."runs"
  DROP COLUMN IF EXISTS "kind";
ALTER TABLE "public"."agents"
  DROP COLUMN IF EXISTS "can_use_workers";
