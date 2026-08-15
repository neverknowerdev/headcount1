-- enforce the team member role domain
ALTER TABLE "public"."team_members"
  ADD CONSTRAINT "ck_team_members_role_enum" CHECK ("role" IN ('owner', 'member'));
