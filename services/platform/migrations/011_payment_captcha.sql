CREATE TABLE payment_captcha_attempts (
 order_id text PRIMARY KEY REFERENCES orders(id),
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','checking','solving','submitted','failed','unconfirmed')),
 challenge_type text NOT NULL DEFAULT '',
 failure_code text NOT NULL DEFAULT '',
 evidence_cipher text NOT NULL DEFAULT '',
 solver_request_id text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
