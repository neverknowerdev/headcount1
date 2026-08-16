
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
