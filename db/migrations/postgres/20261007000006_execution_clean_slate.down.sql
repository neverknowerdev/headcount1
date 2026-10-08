-- Irreversible: the deleted runs, their messages and the model choices that
-- were folded into the tiers cannot be reconstructed. This restores the
-- legacy schema shape for an operator-assisted rollback only; automatic
-- rollback must refuse this migration.
ALTER TABLE "public"."default_model_settings" DROP CONSTRAINT IF EXISTS "ck_default_model_settings_purpose_enum";
ALTER TABLE "public"."default_model_settings" ADD CONSTRAINT "ck_default_model_settings_purpose_enum"
  CHECK ("purpose" IN ('commit_messages', 'task_orchestrator', 'helper_worker', 'smart', 'cheap', 'classifier'));
ALTER TABLE "public"."tasks" DROP CONSTRAINT IF EXISTS "ck_tasks_status_enum";
ALTER TABLE "public"."tasks" ADD CONSTRAINT "ck_tasks_status_enum"
  CHECK ("status" IN ('backlog', 'to-do', 'in-progress', 'blocked', 'depends-on-task', 'in-review', 'refinement', 'done', 'failed', 'canceled'));
ALTER TABLE "public"."tasks" ADD COLUMN "orchestrator_run_id" integer NULL;
CREATE INDEX "idx_tasks_orchestrator_run_id" ON "public"."tasks" ("orchestrator_run_id");
ALTER TABLE "public"."runs" DROP CONSTRAINT IF EXISTS "ck_runs_status_enum";
ALTER TABLE "public"."runs" ADD CONSTRAINT "ck_runs_status_enum"
  CHECK ("status" IN ('running', 'completed', 'failed', 'canceled', 'paused', 'recoverable_failed', 'stale', 'resuming', 'waiting', 'interrupted'));
ALTER TABLE "public"."runs" ADD COLUMN "current_status" text NULL DEFAULT '';
ALTER TABLE "public"."runs" ADD COLUMN "root_run_id" integer NULL;
ALTER TABLE "public"."runs" ADD COLUMN "parent_run_id" integer NULL;
ALTER TABLE "public"."runs" ADD COLUMN "kind" text NOT NULL DEFAULT 'agent_session';
ALTER TABLE "public"."runs" ADD CONSTRAINT "ck_runs_kind_enum"
  CHECK ("kind" IN ('task_orchestrator', 'agent_session', 'ceo_consultation', 'helper_worker'));
CREATE INDEX "idx_runs_parent_run_id" ON "public"."runs" ("parent_run_id");
CREATE INDEX "idx_runs_root_run_id" ON "public"."runs" ("root_run_id");
-- create "run_status_reports" table
CREATE TABLE "public"."run_status_reports" (
  "id" bigserial NOT NULL,
  "run_id" integer NOT NULL,
  "status" text NOT NULL,
  "message_id" bigint NULL,
  "reported_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_run_status_reports_run" FOREIGN KEY ("run_id") REFERENCES "public"."runs" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- create index "idx_run_status_reports_message_id" to table: "run_status_reports"
CREATE INDEX "idx_run_status_reports_message_id" ON "public"."run_status_reports" ("message_id");
-- create index "idx_run_status_reports_reported_at" to table: "run_status_reports"
CREATE INDEX "idx_run_status_reports_reported_at" ON "public"."run_status_reports" ("reported_at");
-- create index "idx_run_status_reports_run_id" to table: "run_status_reports"
CREATE INDEX "idx_run_status_reports_run_id" ON "public"."run_status_reports" ("run_id");
-- create "run_events" table
CREATE TABLE "public"."run_events" (
  "id" bigserial NOT NULL,
  "task_id" integer NOT NULL,
  "run_id" integer NOT NULL,
  "event_type" text NOT NULL,
  "payload" text NULL,
  "dedupe_key" text NULL,
  "created_at" timestamptz NULL,
  "consumed_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
-- create index "idx_run_events_consumed_at" to table: "run_events"
CREATE INDEX "idx_run_events_consumed_at" ON "public"."run_events" ("consumed_at");
-- create index "idx_run_events_created_at" to table: "run_events"
CREATE INDEX "idx_run_events_created_at" ON "public"."run_events" ("created_at");
-- create index "idx_run_events_dedupe_key" to table: "run_events"
CREATE INDEX "idx_run_events_dedupe_key" ON "public"."run_events" ("dedupe_key");
-- create index "idx_run_events_run_id" to table: "run_events"
CREATE INDEX "idx_run_events_run_id" ON "public"."run_events" ("run_id");
-- create index "idx_run_events_task_id" to table: "run_events"
CREATE INDEX "idx_run_events_task_id" ON "public"."run_events" ("task_id");
ALTER TABLE "public"."run_events"
  ADD CONSTRAINT "ck_run_events_event_type_enum" CHECK ("event_type" IN ('run_status', 'status_report', 'status_report_request', 'worker_question'));
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
ALTER TABLE "public"."run_events" DROP CONSTRAINT IF EXISTS "ck_run_events_event_type_enum";
ALTER TABLE "public"."run_events" ADD CONSTRAINT "ck_run_events_event_type_enum"
  CHECK ("event_type" IN ('run_status', 'status_report', 'status_report_request', 'worker_question', 'session_message', 'session_message_answer', 'worker_finished', 'human_input_requested', 'human_input_answered'));
-- create "proxy_request_logs" table
CREATE TABLE "public"."proxy_request_logs" (
  "id" serial NOT NULL,
  "agent_id" integer NOT NULL,
  "provider_id" integer NOT NULL,
  "model" text NOT NULL,
  "prompt_tokens" bigint NULL,
  "completion_tokens" bigint NULL,
  "total_tokens" bigint NULL,
  "created_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_proxy_request_logs_agent" FOREIGN KEY ("agent_id") REFERENCES "public"."agents" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "fk_proxy_request_logs_provider" FOREIGN KEY ("provider_id") REFERENCES "public"."llm_providers" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
