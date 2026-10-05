
-- create "runs" table
CREATE TABLE "public"."runs" (
  "id" serial NOT NULL,
  "task_id" integer NOT NULL,
  "agent_id" integer NOT NULL,
  "name" text NULL,
  "parent_run_id" integer NULL,
  "root_run_id" integer NULL,
  "current_status" text NULL DEFAULT '',
  "status" text NOT NULL,
  "session_id" text NULL,
  "log_file_path" text NULL,
  "log_content" text NULL,
  "log_entries" text NULL,
  "token_stats" text NULL,
  "result_description" text NULL,
  "result_explanation" text NULL,
  "started_at" timestamptz NULL,
  "ended_at" timestamptz NULL,
  "last_message_time" timestamptz NULL,
  "recovery" jsonb NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_runs_agent" FOREIGN KEY ("agent_id") REFERENCES "public"."agents" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "fk_runs_task" FOREIGN KEY ("task_id") REFERENCES "public"."tasks" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- create index "idx_runs_name" to table: "runs"
CREATE INDEX "idx_runs_name" ON "public"."runs" ("name");
-- create index "idx_runs_parent_run_id" to table: "runs"
CREATE INDEX "idx_runs_parent_run_id" ON "public"."runs" ("parent_run_id");
-- create index "idx_runs_root_run_id" to table: "runs"
CREATE INDEX "idx_runs_root_run_id" ON "public"."runs" ("root_run_id");
