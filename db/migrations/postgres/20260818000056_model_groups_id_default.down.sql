ALTER TABLE "public"."model_groups" ALTER COLUMN "id" DROP DEFAULT;
DROP SEQUENCE IF EXISTS "public"."model_groups_id_seq";
