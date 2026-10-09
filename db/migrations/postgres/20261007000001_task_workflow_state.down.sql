-- Irreversible once any task is failed or canceled: those rows cannot be made
-- valid for the older status domain without rewriting user-visible outcomes.
-- Automatic rollback must refuse this migration when such rows exist.
ALTER TABLE "public"."runs" DROP COLUMN IF EXISTS "attempt";
ALTER TABLE "public"."runs" DROP COLUMN IF EXISTS "report";
ALTER TABLE "public"."comments" DROP COLUMN IF EXISTS "reply_to_id";
DROP INDEX IF EXISTS "public"."idx_tasks_origin_step_id";
DROP INDEX IF EXISTS "public"."idx_tasks_root_task_id";
ALTER TABLE "public"."tasks" DROP CONSTRAINT IF EXISTS "ck_tasks_status_enum";
ALTER TABLE "public"."tasks" ADD CONSTRAINT "ck_tasks_status_enum"
  CHECK ("status" IN ('backlog', 'to-do', 'in-progress', 'blocked', 'depends-on-task', 'in-review', 'refinement', 'done'));
ALTER TABLE "public"."tasks" DROP CONSTRAINT IF EXISTS "ck_tasks_result_verdict_enum";
ALTER TABLE "public"."tasks" DROP CONSTRAINT IF EXISTS "ck_tasks_result_reason_enum";
ALTER TABLE "public"."tasks" DROP CONSTRAINT IF EXISTS "ck_tasks_waiting_on_enum";
ALTER TABLE "public"."tasks" DROP CONSTRAINT IF EXISTS "ck_tasks_phase_enum";
ALTER TABLE "public"."tasks" DROP CONSTRAINT IF EXISTS "ck_tasks_mode_enum";
ALTER TABLE "public"."tasks" DROP CONSTRAINT IF EXISTS "ck_tasks_task_type_enum";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "design";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "workspace_owner_task_id";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "review_round";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "attempts_used";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "adjust_cycles_used";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "smart_steps_used";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "result_verdict";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "result_evidence";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "result_details";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "result_summary";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "result_reason";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "model";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "model_group_id";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "provider_id";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "workflow_phase";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "origin_phase";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "origin_step_id";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "depth";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "root_task_id";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "lease_until";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "lease_owner";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "wait_detail";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "wait_until";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "wait_ref";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "waiting_on";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "phase";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "mode";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "task_type";
