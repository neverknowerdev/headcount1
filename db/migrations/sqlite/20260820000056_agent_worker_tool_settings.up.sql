ALTER TABLE `agents` ADD COLUMN `worker_permissions` TEXT NOT NULL DEFAULT '';
ALTER TABLE `agents` ADD COLUMN `worker_allowed_mc_ps` TEXT NOT NULL DEFAULT '';
