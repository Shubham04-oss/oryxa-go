-- Device sign-in (RFC 8628) for the CLI. Only hashes of the device code,
-- the user code and the issued token are stored.
CREATE TABLE auth_cli_devices (
    device_code_hash text PRIMARY KEY,
    user_code_hash text NOT NULL UNIQUE,
    client_name text NOT NULL,
    client_address text NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'denied')),
    subject text NOT NULL DEFAULT '',
    interval_seconds integer NOT NULL,
    last_polled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);
CREATE INDEX auth_cli_devices_expiry ON auth_cli_devices (expires_at);

-- A device-issued token is opaque, so its session names its member.
ALTER TABLE auth_cli_sessions ADD COLUMN subject text NOT NULL DEFAULT '';
