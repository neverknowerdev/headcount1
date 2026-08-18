-- Legacy databases were baselined from the old GORM schema. Some of those
-- schemas have the model_groups primary key but no PostgreSQL sequence/default,
-- so inserts that rely on database-generated IDs fail with a NOT NULL error.
CREATE SEQUENCE IF NOT EXISTS "public"."model_groups_id_seq";
ALTER SEQUENCE "public"."model_groups_id_seq" OWNED BY "public"."model_groups"."id";
ALTER TABLE "public"."model_groups"
  ALTER COLUMN "id" SET DEFAULT nextval('public.model_groups_id_seq'::regclass);
SELECT setval(
  'public.model_groups_id_seq'::regclass,
  GREATEST(COALESCE((SELECT MAX("id") FROM "public"."model_groups"), 0), 1),
  (SELECT COALESCE(MAX("id"), 0) > 0 FROM "public"."model_groups")
);
