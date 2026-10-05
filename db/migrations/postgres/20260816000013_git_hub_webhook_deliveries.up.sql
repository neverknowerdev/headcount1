
-- create "git_hub_webhook_deliveries" table
CREATE TABLE "public"."git_hub_webhook_deliveries" (
  "id" serial NOT NULL,
  "delivery_id" text NULL,
  "event" text NULL,
  "status" text NOT NULL DEFAULT 'processing',
  "forwarded_at" timestamptz NULL,
  "completed_at" timestamptz NULL,
  "last_error" text NULL,
  "attempt_token" text NULL,
  "lease_expires_at" timestamptz NULL,
  "attempts" bigint NOT NULL DEFAULT 0,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
-- create index "idx_git_hub_webhook_deliveries_attempt_token" to table: "git_hub_webhook_deliveries"
CREATE INDEX "idx_git_hub_webhook_deliveries_attempt_token" ON "public"."git_hub_webhook_deliveries" ("attempt_token");
-- create index "idx_git_hub_webhook_deliveries_delivery_id" to table: "git_hub_webhook_deliveries"
CREATE UNIQUE INDEX "idx_git_hub_webhook_deliveries_delivery_id" ON "public"."git_hub_webhook_deliveries" ("delivery_id");
