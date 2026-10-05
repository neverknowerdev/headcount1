-- Create "team_members" table
CREATE TABLE `team_members` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `team_id` integer NOT NULL,
  `user_id` integer NOT NULL,
  `role` text NOT NULL DEFAULT 'member',
  `created_at` datetime NULL,
  CONSTRAINT `fk_team_members_user` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `fk_team_members_team` FOREIGN KEY (`team_id`) REFERENCES `teams` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_team_member" to table: "team_members"
CREATE UNIQUE INDEX `idx_team_member` ON `team_members` (`team_id`, `user_id`);
