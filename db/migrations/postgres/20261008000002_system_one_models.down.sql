-- A group of System One models has no meaning without the kind, and a
-- language model slot cannot hold one: they go.
DELETE FROM "public"."model_groups" WHERE "kind" = 'system_one';
ALTER TABLE "public"."model_groups" DROP CONSTRAINT IF EXISTS "ck_model_groups_kind_enum";
ALTER TABLE "public"."model_groups" DROP COLUMN "kind";
ALTER TABLE "public"."llm_providers" DROP COLUMN "system_one_models";
