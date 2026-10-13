CREATE TABLE catalog_plans (
    id text PRIMARY KEY,
    product_id text NOT NULL,
    name text NOT NULL,
    price_cents bigint NOT NULL CHECK (price_cents BETWEEN 1 AND 10000000),
    enabled boolean NOT NULL DEFAULT false,
    partner_enabled boolean NOT NULL DEFAULT false,
    renewal_only boolean NOT NULL DEFAULT false,
    purchase_note text NOT NULL DEFAULT '',
    delivery_instructions text NOT NULL DEFAULT '',
    sort_order integer NOT NULL DEFAULT 0,
    version bigint NOT NULL DEFAULT 0 CHECK (version >= 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE partners (
    id text PRIMARY KEY CHECK (id <> 'platform'),
    slug text NOT NULL UNIQUE,
    name text NOT NULL,
    active boolean NOT NULL DEFAULT false
);

CREATE TABLE partner_prices (
    partner_id text NOT NULL REFERENCES partners(id),
    plan text NOT NULL REFERENCES catalog_plans(id),
    price_cents bigint NOT NULL CHECK (price_cents BETWEEN 1 AND 10000000),
    cost_cents bigint NOT NULL CHECK (cost_cents > 0 AND cost_cents <= price_cents),
    version bigint NOT NULL DEFAULT 0 CHECK (version >= 0),
    PRIMARY KEY (partner_id, plan)
);

CREATE TABLE support_contacts (
    owner text PRIMARY KEY,
    wechat text NOT NULL DEFAULT '',
    qq text NOT NULL DEFAULT '',
    email text NOT NULL DEFAULT ''
);

CREATE TABLE partner_announcements (
    partner_id text PRIMARY KEY REFERENCES partners(id),
    enabled boolean NOT NULL DEFAULT false,
    placement text NOT NULL CHECK (placement IN ('home','product','both')),
    title text NOT NULL,
    content text NOT NULL,
    version bigint NOT NULL DEFAULT 0 CHECK (version >= 0)
);

CREATE TABLE partner_product_order (
    partner_id text NOT NULL REFERENCES partners(id),
    product_id text NOT NULL,
    position integer NOT NULL CHECK (position >= 0),
    PRIMARY KEY (partner_id, product_id),
    UNIQUE (partner_id, position)
);

CREATE TABLE worker_heartbeats (
    id text PRIMARY KEY,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- No products, customers or credentials are copied from production.
