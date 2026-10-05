ALTER TABLE `task_relations` ADD COLUMN `__enum_guard_kind` INTEGER NOT NULL DEFAULT 1 CHECK (`kind` IN ('depends_on', 'related_to'));
