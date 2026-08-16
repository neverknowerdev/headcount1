
-- create "model_request_stats" table
CREATE TABLE "public"."model_request_stats" (
  "id" serial NOT NULL,
  "group_id" integer NULL,
  "provider_id" integer NOT NULL,
  "model" text NOT NULL,
  "success" boolean NOT NULL DEFAULT false,
  "rate_limited" boolean NOT NULL DEFAULT false,
  "status_code" bigint NULL,
  "duration_ms" bigint NULL,
  "prompt_tokens" bigint NULL,
  "completion_tokens" bigint NULL,
  "tokens_per_sec" numeric NULL,
  "cooldown_until" timestamptz NULL,
  "error_message" text NULL,
  "created_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
-- create index "idx_model_request_stats_created_at" to table: "model_request_stats"
CREATE INDEX "idx_model_request_stats_created_at" ON "public"."model_request_stats" ("created_at");
-- create index "idx_model_request_stats_group_id" to table: "model_request_stats"
CREATE INDEX "idx_model_request_stats_group_id" ON "public"."model_request_stats" ("group_id");
-- create index "idx_model_request_stats_model" to table: "model_request_stats"
CREATE INDEX "idx_model_request_stats_model" ON "public"."model_request_stats" ("model");
-- create index "idx_model_request_stats_provider_id" to table: "model_request_stats"
CREATE INDEX "idx_model_request_stats_provider_id" ON "public"."model_request_stats" ("provider_id");
