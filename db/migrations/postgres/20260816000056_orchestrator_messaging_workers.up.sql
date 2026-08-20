ALTER TABLE "public"."agents" ADD COLUMN "can_use_workers" boolean NOT NULL DEFAULT false;
UPDATE "public"."agents" SET "can_use_workers" = true WHERE lower(trim("role_key")) IN ('ceo', 'cto', 'cmo');

ALTER TABLE "public"."runs" ADD COLUMN "kind" text NOT NULL DEFAULT 'agent_session';
UPDATE "public"."runs" SET "kind" = 'task_orchestrator'
WHERE "id" IN (SELECT "orchestrator_run_id" FROM "public"."tasks" WHERE "orchestrator_run_id" IS NOT NULL);
ALTER TABLE "public"."runs" ADD CONSTRAINT "ck_runs_kind_enum"
  CHECK ("kind" IN ('task_orchestrator', 'agent_session', 'ceo_consultation', 'helper_worker'));

ALTER TABLE "public"."run_events" ADD COLUMN "source_run_id" integer;
ALTER TABLE "public"."run_events" ADD COLUMN "target_run_id" integer;
ALTER TABLE "public"."run_events" ADD COLUMN "reply_to_event_id" bigint;
UPDATE "public"."run_events" SET "source_run_id" = "run_id" WHERE "source_run_id" IS NULL;
ALTER TABLE "public"."run_events" DROP CONSTRAINT IF EXISTS "ck_run_events_event_type_enum";
ALTER TABLE "public"."run_events" ADD CONSTRAINT "ck_run_events_event_type_enum"
  CHECK ("event_type" IN ('run_status', 'status_report', 'status_report_request', 'worker_question', 'session_message', 'session_message_answer', 'worker_finished'));

CREATE INDEX "idx_run_events_target_pending" ON "public"."run_events" ("target_run_id", "consumed_at", "created_at");
CREATE INDEX "idx_run_events_reply_to" ON "public"."run_events" ("reply_to_event_id");
CREATE UNIQUE INDEX "idx_run_events_dedupe_nonempty" ON "public"."run_events" ("dedupe_key") WHERE "dedupe_key" <> '';
CREATE UNIQUE INDEX "idx_run_events_reply_unique" ON "public"."run_events" ("reply_to_event_id") WHERE "reply_to_event_id" IS NOT NULL;

ALTER TABLE "public"."default_model_settings" DROP CONSTRAINT IF EXISTS "ck_default_model_settings_purpose_enum";
ALTER TABLE "public"."default_model_settings" ADD CONSTRAINT "ck_default_model_settings_purpose_enum"
  CHECK ("purpose" IN ('commit_messages', 'ask_artifact', 'task_orchestrator', 'helper_worker'));
