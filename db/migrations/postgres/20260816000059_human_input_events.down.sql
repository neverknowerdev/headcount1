ALTER TABLE "public"."run_events" DROP CONSTRAINT IF EXISTS "ck_run_events_event_type_enum";
ALTER TABLE "public"."run_events" ADD CONSTRAINT "ck_run_events_event_type_enum"
  CHECK ("event_type" IN ('run_status', 'status_report', 'status_report_request', 'worker_question', 'session_message', 'session_message_answer', 'worker_finished'));
