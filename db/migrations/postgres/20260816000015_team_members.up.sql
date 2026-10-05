
-- create "team_members" table
CREATE TABLE "public"."team_members" (
  "id" serial NOT NULL,
  "team_id" integer NOT NULL,
  "user_id" integer NOT NULL,
  "role" text NOT NULL DEFAULT 'member',
  "created_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_team_members_team" FOREIGN KEY ("team_id") REFERENCES "public"."teams" ("id") ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT "fk_team_members_user" FOREIGN KEY ("user_id") REFERENCES "public"."users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- create index "idx_team_member" to table: "team_members"
CREATE UNIQUE INDEX "idx_team_member" ON "public"."team_members" ("team_id", "user_id");
