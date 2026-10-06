ALTER TABLE `agents` ADD COLUMN `builtin` numeric NOT NULL DEFAULT false;
ALTER TABLE `agents` ADD COLUMN `enabled` numeric NOT NULL DEFAULT true;
