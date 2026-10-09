-- A model failure was a crashed session before it had a reason of its own.
UPDATE "public"."tasks" SET "result_reason" = 'run_error' WHERE "result_reason" = 'model_error';
ALTER TABLE "public"."tasks" DROP CONSTRAINT IF EXISTS "ck_tasks_result_reason_enum";
ALTER TABLE "public"."tasks" ADD CONSTRAINT "ck_tasks_result_reason_enum"
  CHECK ("result_reason" IN ('', 'reported_failure', 'cannot_complete', 'run_error', 'budget_exhausted', 'prerequisite_failed', 'stopped'));
