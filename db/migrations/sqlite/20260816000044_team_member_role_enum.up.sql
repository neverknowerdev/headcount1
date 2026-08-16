ALTER TABLE `team_members` ADD COLUMN `__enum_guard_role` INTEGER NOT NULL DEFAULT 1 CHECK (`role` IN ('owner', 'member'));
