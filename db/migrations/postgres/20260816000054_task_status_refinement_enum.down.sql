ALTER TABLE "public"."tasks"
  DROP CONSTRAINT IF EXISTS "ck_tasks_status_enum";
ALTER TABLE "public"."tasks"
  ADD CONSTRAINT "ck_tasks_status_enum" CHECK ("status" IN ('backlog', 'to-do', 'in-progress', 'blocked', 'depends-on-task', 'in-review', 'done'));
