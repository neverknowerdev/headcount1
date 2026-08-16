-- enforce the team invite role domain
ALTER TABLE "public"."team_invites"
  ADD CONSTRAINT "ck_team_invites_role_enum" CHECK ("role" IN ('owner', 'member'));
