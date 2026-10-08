-- Decisions recorded by smart steps and executor checkpoints. The hierarchy
-- follows the task tree and, within a task, parent_decision_id.
CREATE TABLE "public"."decisions" (
  "id" bigserial NOT NULL,
  "task_id" integer NOT NULL,
  "root_task_id" integer NOT NULL DEFAULT 0,
  "parent_decision_id" bigint NULL,
  "step_id" bigint NULL,
  "run_id" integer NULL,
  "log_seq" bigint NULL,
  "phase" text NOT NULL DEFAULT '',
  "agent_id" integer NULL,
  "kind" text NOT NULL DEFAULT 'decision',
  "title" text NOT NULL,
  "decision" text NOT NULL DEFAULT '',
  "rationale" text NOT NULL DEFAULT '',
  "alternatives" text NOT NULL DEFAULT '',
  "supersedes_id" bigint NULL,
  "created_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "ck_decisions_kind_enum" CHECK ("kind" IN ('decision', 'assumption', 'dead_end')),
  CONSTRAINT "fk_decisions_task" FOREIGN KEY ("task_id") REFERENCES "public"."tasks" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
CREATE INDEX "idx_decisions_task_id" ON "public"."decisions" ("task_id", "id");
CREATE INDEX "idx_decisions_root_task_id" ON "public"."decisions" ("root_task_id");
