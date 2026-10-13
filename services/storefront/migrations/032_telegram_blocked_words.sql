ALTER TABLE telegram_group_moderation
 ADD COLUMN blocked_words text[] NOT NULL DEFAULT '{}'::text[] CHECK(cardinality(blocked_words)<=100 AND array_position(blocked_words,NULL) IS NULL),
 ADD COLUMN words_last_update_id bigint;
