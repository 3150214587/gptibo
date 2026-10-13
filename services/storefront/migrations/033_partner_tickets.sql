CREATE TABLE partner_tickets (
    id text PRIMARY KEY,
    partner_id text NOT NULL REFERENCES partners(id),
    order_id text NOT NULL REFERENCES orders(id),
    subject text NOT NULL CHECK (char_length(subject) BETWEEN 1 AND 120),
    description text NOT NULL CHECK (char_length(description) BETWEEN 1 AND 5000),
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open','processing','waiting','resolved')),
    version bigint NOT NULL DEFAULT 0,
    created_at bigint NOT NULL,
    updated_at bigint NOT NULL
);
CREATE INDEX partner_tickets_owner ON partner_tickets(partner_id,updated_at DESC,id);
CREATE INDEX partner_tickets_queue ON partner_tickets(status,updated_at DESC,id);
CREATE TABLE partner_ticket_events (
    id text PRIMARY KEY,
    ticket_id text NOT NULL REFERENCES partner_tickets(id),
    actor_role text NOT NULL CHECK (actor_role IN ('admin','partner')),
    actor_id text NOT NULL,
    body text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 5000),
    status text NOT NULL CHECK (status IN ('open','processing','waiting','resolved')),
    version bigint NOT NULL,
    created_at bigint NOT NULL,
    UNIQUE(ticket_id,version)
);
-- A transactional counter avoids missing notifications from out-of-order commits.
CREATE TABLE partner_ticket_notifications (
    id integer PRIMARY KEY CHECK (id=1),
    version bigint NOT NULL DEFAULT 0
);
INSERT INTO partner_ticket_notifications(id) VALUES(1);
