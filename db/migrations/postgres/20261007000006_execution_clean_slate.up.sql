-- Execution clean slate. The orchestrator-and-workers engine is gone; this
-- removes its runs, its messaging tables and its columns. Companies, projects,
-- tasks, agents, providers, model groups and artifacts are kept.

-- Artifacts are deliverables and must outlive the runs that wrote them, so
-- their link to a run becomes optional before any run is deleted.
ALTER TABLE "public"."artifacts" ALTER COLUMN "run_id" DROP NOT NULL;
ALTER TABLE "public"."artifacts" DROP CONSTRAINT IF EXISTS "fk_artifacts_run";
ALTER TABLE "public"."artifacts" ADD CONSTRAINT "fk_artifacts_run"
  FOREIGN KEY ("run_id") REFERENCES "public"."runs" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;

-- Old runs and everything that only described them.
UPDATE "public"."comments" SET "run_id" = NULL WHERE "run_id" IS NOT NULL;
DROP TABLE IF EXISTS "public"."run_status_reports";
DROP TABLE IF EXISTS "public"."run_events";
DROP TABLE IF EXISTS "public"."proxy_request_logs";
DELETE FROM "public"."runs";

-- A run is now one executor session: no kinds, no tree of sessions, no
-- self-reported status line.
DROP INDEX IF EXISTS "public"."idx_runs_root_run_id";
DROP INDEX IF EXISTS "public"."idx_runs_parent_run_id";
ALTER TABLE "public"."runs" DROP CONSTRAINT IF EXISTS "ck_runs_kind_enum";
ALTER TABLE "public"."runs" DROP COLUMN IF EXISTS "kind";
ALTER TABLE "public"."runs" DROP COLUMN IF EXISTS "parent_run_id";
ALTER TABLE "public"."runs" DROP COLUMN IF EXISTS "root_run_id";
ALTER TABLE "public"."runs" DROP COLUMN IF EXISTS "current_status";
ALTER TABLE "public"."runs" DROP CONSTRAINT IF EXISTS "ck_runs_status_enum";
ALTER TABLE "public"."runs" ADD CONSTRAINT "ck_runs_status_enum"
  CHECK ("status" IN ('running', 'paused', 'resuming', 'completed', 'failed', 'canceled'));

-- Tasks that were mid-run go back to the backlog: their runs are gone.
-- Tasks in to-do, in review or done are left as they are.
UPDATE "public"."tasks"
SET "status" = 'backlog', "phase" = '', "waiting_on" = '', "wait_ref" = NULL, "wait_until" = NULL,
    "wait_detail" = '', "lease_owner" = '', "lease_until" = NULL, "run_id" = NULL
WHERE "status" IN ('in-progress', 'refinement', 'blocked', 'depends-on-task');
UPDATE "public"."tasks" SET "run_id" = NULL WHERE "run_id" IS NOT NULL;
DROP INDEX IF EXISTS "public"."idx_tasks_orchestrator_run_id";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "orchestrator_run_id";
ALTER TABLE "public"."tasks" DROP CONSTRAINT IF EXISTS "ck_tasks_status_enum";
ALTER TABLE "public"."tasks" ADD CONSTRAINT "ck_tasks_status_enum"
  CHECK ("status" IN ('backlog', 'to-do', 'in-progress', 'blocked', 'depends-on-task', 'in-review', 'done', 'failed', 'canceled'));

-- The cheap tier takes over the helper-worker model, or failing that the
-- orchestrator's: both were the cheap models of the old design.
UPDATE "public"."default_model_settings" SET "purpose" = 'cheap'
WHERE "purpose" = 'helper_worker' AND ("provider_id" IS NOT NULL OR "model_group_id" IS NOT NULL)
  AND NOT EXISTS (SELECT 1 FROM "public"."default_model_settings" AS other WHERE other."user_id" = "public"."default_model_settings"."user_id" AND other."purpose" = 'cheap');
UPDATE "public"."default_model_settings" SET "purpose" = 'cheap'
WHERE "purpose" = 'task_orchestrator' AND ("provider_id" IS NOT NULL OR "model_group_id" IS NOT NULL)
  AND NOT EXISTS (SELECT 1 FROM "public"."default_model_settings" AS other WHERE other."user_id" = "public"."default_model_settings"."user_id" AND other."purpose" = 'cheap');
DELETE FROM "public"."default_model_settings" WHERE "purpose" IN ('task_orchestrator', 'helper_worker');
-- The smart tier takes the model the user's CEO agent ran on: agents were
-- where the strong models were configured.
INSERT INTO "public"."default_model_settings" ("purpose", "user_id", "provider_id", "model", "model_group_id", "created_at", "updated_at")
SELECT 'smart', company."user_id", agent."provider_id", COALESCE(agent."model", ''), agent."model_group_id", CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
FROM "public"."agents" AS agent JOIN "public"."companies" AS company ON company."id" = agent."company_id"
WHERE agent."id" IN (
    SELECT MIN(a."id") FROM "public"."agents" AS a JOIN "public"."companies" AS co ON co."id" = a."company_id"
    WHERE co."user_id" IS NOT NULL AND lower(trim(a."role_key")) = 'ceo'
      AND (a."provider_id" IS NOT NULL OR a."model_group_id" IS NOT NULL)
    GROUP BY co."user_id")
  AND NOT EXISTS (SELECT 1 FROM "public"."default_model_settings" AS other WHERE other."user_id" = company."user_id" AND other."purpose" = 'smart');
ALTER TABLE "public"."default_model_settings" DROP CONSTRAINT IF EXISTS "ck_default_model_settings_purpose_enum";
ALTER TABLE "public"."default_model_settings" ADD CONSTRAINT "ck_default_model_settings_purpose_enum"
  CHECK ("purpose" IN ('commit_messages', 'smart', 'cheap', 'classifier'));
