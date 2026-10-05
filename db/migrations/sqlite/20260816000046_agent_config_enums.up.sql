ALTER TABLE `agents` ADD COLUMN `__enum_guard_chat_type` INTEGER NOT NULL DEFAULT 1 CHECK (`chat_type` IN ('message_history', 'compact_thinking'));
ALTER TABLE `agents` ADD COLUMN `__enum_guard_reasoning_level` INTEGER NOT NULL DEFAULT 1 CHECK (`reasoning_level` IS NULL OR `reasoning_level` IN ('', 'low', 'medium', 'max'));
