-- enforce the agent configuration domains
ALTER TABLE "public"."agents"
  ADD CONSTRAINT "ck_agents_chat_type_enum" CHECK ("chat_type" IN ('message_history', 'compact_thinking'));
ALTER TABLE "public"."agents"
  ADD CONSTRAINT "ck_agents_reasoning_level_enum" CHECK ("reasoning_level" IS NULL OR "reasoning_level" IN ('', 'low', 'medium', 'max'));
