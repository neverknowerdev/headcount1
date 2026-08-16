
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
