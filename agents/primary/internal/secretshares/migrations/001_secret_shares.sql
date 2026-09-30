-- One-time secrets. When the answer to a Slack request is a credential, its
-- owner types it into Oryxa, and the member who asked opens it once, in their
-- own session; it is never in Slack, in an event, in the ledger or in a model's
-- context. This table is where it waits in between.
--
-- A row is one secret for one request (request_id is the key, so sharing again
-- replaces it). The value is never here in the clear: sealed is the value
-- envelope-encrypted under the agents-secrets KMS key, bound to the request it
-- was sealed for, so it opens only with a call to KMS that names that request.
-- The other columns are who shared it, who may open it and when it stops being
-- there; none of them is secret.
--
-- Opening deletes the row, in the transaction that records the view on the
-- request. Revoking deletes it. A row that expired is refused, and deleted, by
-- whichever call meets it first, and the sweeper removes the rest, so no
-- ciphertext outlives its hour by more than a minute or so.
--
-- There is no foreign key to agent_interactions: the stores migrate on their
-- own, and a member who leaves or an organization that is deleted has its rows
-- removed by Forget, which needs no request to find them by. Every statement is
-- safe to repeat.

CREATE SCHEMA IF NOT EXISTS agents_primary;

CREATE TABLE IF NOT EXISTS agents_primary.secret_shares (
    request_id text PRIMARY KEY,
    organization_id text NOT NULL,
    owner_user_id text NOT NULL,
    asker_user_id text NOT NULL,
    -- secrets.SealedData as JSON: version, nonce, ciphertext and the wrapped
    -- data key. Nothing else is stored of the value.
    sealed jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    -- A secret lives an hour at most (interactions.MaxSecretTTL). The database
    -- holds that too, so a bug elsewhere cannot keep one longer.
    CONSTRAINT secret_shares_lifetime_check CHECK (
        expires_at > created_at AND expires_at <= created_at + interval '1 hour'),
    CONSTRAINT secret_shares_parties_check CHECK (
        organization_id <> '' AND owner_user_id <> '' AND asker_user_id <> '')
);

-- The sweeper's, oldest first.
CREATE INDEX IF NOT EXISTS secret_shares_expiry_idx
    ON agents_primary.secret_shares (expires_at);
