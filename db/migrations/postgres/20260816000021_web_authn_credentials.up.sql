
-- create "web_authn_credentials" table
CREATE TABLE "public"."web_authn_credentials" (
  "id" serial NOT NULL,
  "user_id" integer NOT NULL,
  "credential_id" bytea NOT NULL,
  "public_key" bytea NOT NULL,
  "sign_count" bigint NULL,
  "transports" text NULL,
  "aa_guid" bytea NULL,
  "backup_eligible" boolean NULL,
  "backup_state" boolean NULL,
  "nickname" text NULL,
  "wrapped_dek" text NOT NULL,
  "prf_salt" bytea NOT NULL,
  "last_used_at" timestamptz NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "fk_web_authn_credentials_user" FOREIGN KEY ("user_id") REFERENCES "public"."users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE
);
-- create index "idx_web_authn_credentials_credential_id" to table: "web_authn_credentials"
CREATE UNIQUE INDEX "idx_web_authn_credentials_credential_id" ON "public"."web_authn_credentials" ("credential_id");
-- create index "idx_web_authn_credentials_user_id" to table: "web_authn_credentials"
CREATE INDEX "idx_web_authn_credentials_user_id" ON "public"."web_authn_credentials" ("user_id");
