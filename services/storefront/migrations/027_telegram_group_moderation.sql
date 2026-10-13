CREATE TABLE telegram_group_moderation (
 group_id bigint PRIMARY KEY CHECK(group_id<0),
 muted boolean NOT NULL DEFAULT false,
 target_muted boolean NOT NULL DEFAULT false,
 previous_permissions jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(jsonb_typeof(previous_permissions)='object'),
 last_update_id bigint,
 updated_at timestamptz NOT NULL DEFAULT now()
);
