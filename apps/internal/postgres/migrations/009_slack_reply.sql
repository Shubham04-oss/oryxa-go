-- "Reply yourself": an owner's own answer, sent from the Slack modal the Reply
-- yourself button of their notice or review opened, is a third kind of inbound
-- record, 'reply'. It holds the words, the button's ticket and the message the
-- button was on, and is forgotten 7 days after it arrived, as every record is.
--
-- 008's kind check is replaced under its own name by one that also knows
-- 'reply': dropped only while it does not, so a database that ran this already
-- keeps the new one, and added NOT VALID, so nothing is scanned under the lock
-- the change takes. The rows it then validates are a week's at most. Safe to
-- apply again.

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'apps_slack_inbound_kind_check'
          AND conrelid = 'apps_slack_inbound'::regclass
          AND pg_get_constraintdef(oid) NOT LIKE '%reply%'
    ) THEN
        ALTER TABLE apps_slack_inbound DROP CONSTRAINT apps_slack_inbound_kind_check;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'apps_slack_inbound_kind_check'
          AND conrelid = 'apps_slack_inbound'::regclass
    ) THEN
        ALTER TABLE apps_slack_inbound
            ADD CONSTRAINT apps_slack_inbound_kind_check CHECK (kind IN ('message', 'action', 'reply')) NOT VALID;
    END IF;
END $$;

ALTER TABLE apps_slack_inbound VALIDATE CONSTRAINT apps_slack_inbound_kind_check;
