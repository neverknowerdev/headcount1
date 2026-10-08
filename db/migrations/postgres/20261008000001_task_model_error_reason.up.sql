-- model_error: a task whose model could not be called. It was never attempted,
-- so its parent does not re-plan around it; the problem goes to the human.
ALTER TABLE "public"."tasks" DROP CONSTRAINT IF EXISTS "ck_tasks_result_reason_enum";
ALTER TABLE "public"."tasks" ADD CONSTRAINT "ck_tasks_result_reason_enum"
  CHECK ("result_reason" IN ('', 'reported_failure', 'cannot_complete', 'run_error', 'budget_exhausted', 'prerequisite_failed', 'stopped', 'model_error'));
