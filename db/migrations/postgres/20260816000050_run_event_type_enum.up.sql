ALTER TABLE "public"."run_events"
  ADD CONSTRAINT "ck_run_events_event_type_enum" CHECK ("event_type" IN ('run_status', 'status_report', 'status_report_request', 'worker_question'));
