-- Create "team_invites" table
CREATE TABLE `team_invites` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `team_id` integer NOT NULL,
  `email` text NOT NULL,
  `role` text NOT NULL DEFAULT 'member',
  `token_hash` text NOT NULL,
  `invited_by` integer NOT NULL,
  `expires_at` datetime NOT NULL,
  `accepted_at` datetime NULL,
  `created_at` datetime NULL,
  CONSTRAINT `fk_team_invites_team` FOREIGN KEY (`team_id`) REFERENCES `teams` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_team_invites_token_hash" to table: "team_invites"
CREATE UNIQUE INDEX `idx_team_invites_token_hash` ON `team_invites` (`token_hash`);
-- Create index "idx_team_invites_team_id" to table: "team_invites"
CREATE INDEX `idx_team_invites_team_id` ON `team_invites` (`team_id`);
