
-- create "team_invites" table
CREATE TABLE "public"."team_invites" (
  "id" serial NOT NULL,
  "team_id" integer NOT NULL,
  "email" text NOT NULL,
  "role" text NOT NULL DEFAULT 'member',
  "token_hash" text NOT NULL,
  "invited_by" integer NOT NULL,
  "expires_at" timestamptz NOT NULL,
  "accepted_at" timestamptz NULL,
  "created_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_team_invites_team" FOREIGN KEY ("team_id") REFERENCES "public"."teams" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- create index "idx_team_invites_team_id" to table: "team_invites"
CREATE INDEX "idx_team_invites_team_id" ON "public"."team_invites" ("team_id");
-- create index "idx_team_invites_token_hash" to table: "team_invites"
CREATE UNIQUE INDEX "idx_team_invites_token_hash" ON "public"."team_invites" ("token_hash");
