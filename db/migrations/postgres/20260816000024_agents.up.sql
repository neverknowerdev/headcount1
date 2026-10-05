
-- create "agents" table
CREATE TABLE "public"."agents" (
  "id" serial NOT NULL,
  "company_id" integer NOT NULL,
  "name" text NOT NULL,
  "role_key" text NULL DEFAULT '',
  "short_name" text NULL DEFAULT '',
  "description" text NULL,
  "system_prompt" text NOT NULL,
  "provider_id" integer NULL,
  "model_group_id" integer NULL,
  "model" text NULL,
  "mode" text NOT NULL DEFAULT 'primary',
  "chat_type" text NOT NULL DEFAULT 'message_history',
  "reasoning_level" text NULL DEFAULT '',
  "subagents" text NULL DEFAULT '',
  "allowed_mc_ps" text NULL DEFAULT '',
  "permissions" text NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_agents_company" FOREIGN KEY ("company_id") REFERENCES "public"."companies" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "fk_agents_model_group" FOREIGN KEY ("model_group_id") REFERENCES "public"."model_groups" ("id") ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT "fk_agents_provider" FOREIGN KEY ("provider_id") REFERENCES "public"."llm_providers" ("id") ON UPDATE NO ACTION ON DELETE SET NULL
);
-- create index "idx_agents_role_key" to table: "agents"
CREATE INDEX "idx_agents_role_key" ON "public"."agents" ("role_key");
