
-- create "git_hub_webhook_targets" table
CREATE TABLE "public"."git_hub_webhook_targets" (
  "id" serial NOT NULL,
  "delivery_id" text NOT NULL,
  "task_id" integer NOT NULL,
  "comment_id" integer NOT NULL,
  "wake_status" text NOT NULL DEFAULT 'pending',
  "wake_attempt_token" text NULL,
  "wake_lease_expires_at" timestamptz NULL,
  "wake_attempts" bigint NOT NULL DEFAULT 0,
  "wake_last_error" text NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
-- create index "idx_git_hub_webhook_targets_wake_attempt_token" to table: "git_hub_webhook_targets"
CREATE INDEX "idx_git_hub_webhook_targets_wake_attempt_token" ON "public"."git_hub_webhook_targets" ("wake_attempt_token");
-- create index "idx_github_delivery_task" to table: "git_hub_webhook_targets"
CREATE UNIQUE INDEX "idx_github_delivery_task" ON "public"."git_hub_webhook_targets" ("delivery_id", "task_id");
