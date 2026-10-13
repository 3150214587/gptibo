ALTER TABLE partner_branding
 ADD COLUMN page_title text NOT NULL DEFAULT '' CHECK(char_length(page_title)<=80),
 ADD COLUMN headline text NOT NULL DEFAULT '' CHECK(char_length(headline)<=80),
 ADD COLUMN description text NOT NULL DEFAULT '' CHECK(char_length(description)<=200);
ALTER TABLE recharge_redemptions
 ADD COLUMN public_message text NOT NULL DEFAULT '' CHECK(char_length(public_message)<=500),
 ADD COLUMN recovery_action text NOT NULL DEFAULT '' CHECK(recovery_action IN ('','retry','refund'));
