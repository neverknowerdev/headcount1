ALTER TABLE "public"."agents" ADD COLUMN "mode" text NOT NULL DEFAULT 'primary';
ALTER TABLE "public"."agents" ADD COLUMN "subagents" text NOT NULL DEFAULT '';
ALTER TABLE "public"."tasks" ADD COLUMN "task_type" text NOT NULL DEFAULT 'plan and implement';
ALTER TABLE "public"."tasks" ADD CONSTRAINT "ck_tasks_task_type_enum" CHECK ("task_type" IN ('plan and implement', 'implement'));
