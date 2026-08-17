ALTER TABLE "public"."agents" DROP COLUMN IF EXISTS "mode";
ALTER TABLE "public"."agents" DROP COLUMN IF EXISTS "subagents";
ALTER TABLE "public"."tasks" DROP CONSTRAINT IF EXISTS "ck_tasks_task_type_enum";
ALTER TABLE "public"."tasks" DROP COLUMN IF EXISTS "task_type";
