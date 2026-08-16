-- enforce the task type and lifecycle domains
ALTER TABLE "public"."tasks"
  ADD CONSTRAINT "ck_tasks_task_type_enum" CHECK ("task_type" IN ('plan and implement', 'implement'));
ALTER TABLE "public"."tasks"
  ADD CONSTRAINT "ck_tasks_status_enum" CHECK ("status" IN ('backlog', 'to-do', 'in-progress', 'blocked', 'depends-on-task', 'in-review', 'done'));
