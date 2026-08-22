-- The dropped columns' row values cannot be reconstructed after the up
-- migration. This irreversible rollback restores the legacy schema shape for an operator-assisted
-- rollback only; automatic rollback must refuse this migration.
ALTER TABLE "public"."agents" ADD COLUMN "mode" text NOT NULL DEFAULT 'primary';
ALTER TABLE "public"."agents" ADD COLUMN "subagents" text NOT NULL DEFAULT '';
ALTER TABLE "public"."tasks" ADD COLUMN "task_type" text NOT NULL DEFAULT 'plan and implement';
ALTER TABLE "public"."tasks" ADD CONSTRAINT "ck_tasks_task_type_enum" CHECK ("task_type" IN ('plan and implement', 'implement'));
