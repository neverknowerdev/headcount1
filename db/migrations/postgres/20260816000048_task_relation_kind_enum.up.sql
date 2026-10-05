ALTER TABLE "public"."task_relations"
  ADD CONSTRAINT "ck_task_relations_kind_enum" CHECK ("kind" IN ('depends_on', 'related_to'));
