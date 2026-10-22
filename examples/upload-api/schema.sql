CREATE TABLE IF NOT EXISTS customers (
    email      text PRIMARY KEY,
    name       text NOT NULL,
    age        integer CHECK (age BETWEEN 0 AND 130),
    country    text CHECK (country ~ '^[A-Z]{2}$'),
    plan       text NOT NULL DEFAULT 'free' CHECK (plan IN ('free', 'pro', 'enterprise')),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE OR REPLACE FUNCTION customers_touch() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS customers_touch ON customers;
CREATE TRIGGER customers_touch BEFORE UPDATE ON customers
    FOR EACH ROW EXECUTE FUNCTION customers_touch();
