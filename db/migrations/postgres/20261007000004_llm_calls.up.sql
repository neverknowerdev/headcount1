-- The usage ledger: one row per model call of any kind, including failed and
-- rejected ones. task_id/agent_id are plain references so usage history
-- outlives the rows it describes; the company owns the ledger.
CREATE TABLE "public"."llm_calls" (
  "id" bigserial NOT NULL,
  "company_id" integer NOT NULL,
  "root_task_id" integer NULL,
  "task_id" integer NULL,
  "agent_id" integer NULL,
  "agent_name" text NOT NULL DEFAULT '',
  "tier" text NOT NULL,
  "purpose" text NOT NULL DEFAULT '',
  "phase" text NOT NULL DEFAULT '',
  "workflow_phase" text NOT NULL DEFAULT '',
  "provider_id" integer NULL,
  "provider_name" text NOT NULL DEFAULT '',
  "model" text NOT NULL DEFAULT '',
  "requested_model" text NOT NULL DEFAULT '',
  "step_id" bigint NULL,
  "run_id" integer NULL,
  "log_seq" bigint NULL,
  "prompt_tokens" integer NOT NULL DEFAULT 0,
  "completion_tokens" integer NOT NULL DEFAULT 0,
  "reasoning_tokens" integer NOT NULL DEFAULT 0,
  "cached_tokens" integer NOT NULL DEFAULT 0,
  "duration_ms" bigint NOT NULL DEFAULT 0,
  "status" text NOT NULL DEFAULT 'ok',
  "error" text NOT NULL DEFAULT '',
  "created_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "ck_llm_calls_tier_enum" CHECK ("tier" IN ('smart', 'cheap', 'classifier', 'commit')),
  CONSTRAINT "ck_llm_calls_status_enum" CHECK ("status" IN ('ok', 'error', 'rejected')),
  CONSTRAINT "fk_llm_calls_company" FOREIGN KEY ("company_id") REFERENCES "public"."companies" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
CREATE INDEX "idx_llm_calls_company_created" ON "public"."llm_calls" ("company_id", "created_at");
CREATE INDEX "idx_llm_calls_root_task_id" ON "public"."llm_calls" ("root_task_id");
CREATE INDEX "idx_llm_calls_task_id" ON "public"."llm_calls" ("task_id");
CREATE INDEX "idx_llm_calls_agent_id" ON "public"."llm_calls" ("agent_id");
CREATE INDEX "idx_llm_calls_model" ON "public"."llm_calls" ("model");
CREATE INDEX "idx_llm_calls_step_id" ON "public"."llm_calls" ("step_id");
CREATE INDEX "idx_llm_calls_run_id" ON "public"."llm_calls" ("run_id");
