-- System One models (TypeSafe's Jev) are classifiers, not language models.
-- A provider may serve both kinds, so it keeps them in separate catalogs, and
-- a model group routes between models of one kind only.
ALTER TABLE "public"."llm_providers" ADD COLUMN "system_one_models" text NOT NULL DEFAULT '';
ALTER TABLE "public"."model_groups" ADD COLUMN "kind" text NOT NULL DEFAULT 'llm';
ALTER TABLE "public"."model_groups" ADD CONSTRAINT "ck_model_groups_kind_enum"
  CHECK ("kind" IN ('llm', 'system_one'));
