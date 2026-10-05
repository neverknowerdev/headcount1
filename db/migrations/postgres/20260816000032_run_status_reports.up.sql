
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
