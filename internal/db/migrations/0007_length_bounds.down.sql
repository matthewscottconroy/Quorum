ALTER TABLE resources
    DROP CONSTRAINT IF EXISTS resources_title_len,
    DROP CONSTRAINT IF EXISTS resources_desc_len,
    DROP CONSTRAINT IF EXISTS resources_url_len,
    DROP CONSTRAINT IF EXISTS resources_cat_len;

ALTER TABLE contacts
    DROP CONSTRAINT IF EXISTS contacts_name_len,
    DROP CONSTRAINT IF EXISTS contacts_org_len,
    DROP CONSTRAINT IF EXISTS contacts_email_len,
    DROP CONSTRAINT IF EXISTS contacts_phone_len,
    DROP CONSTRAINT IF EXISTS contacts_addr_len,
    DROP CONSTRAINT IF EXISTS contacts_cat_len,
    DROP CONSTRAINT IF EXISTS contacts_notes_len;

ALTER TABLE plan_decisions
    DROP CONSTRAINT IF EXISTS pdec_summary_len,
    DROP CONSTRAINT IF EXISTS pdec_rationale_len;

ALTER TABLE plans
    DROP CONSTRAINT IF EXISTS plans_title_len,
    DROP CONSTRAINT IF EXISTS plans_desc_len;

ALTER TABLE action_items
    DROP CONSTRAINT IF EXISTS ai_title_len,
    DROP CONSTRAINT IF EXISTS ai_desc_len;

ALTER TABLE meeting_decisions
    DROP CONSTRAINT IF EXISTS mdec_summary_len,
    DROP CONSTRAINT IF EXISTS mdec_detail_len;

ALTER TABLE meetings
    DROP CONSTRAINT IF EXISTS meetings_title_len,
    DROP CONSTRAINT IF EXISTS meetings_location_len,
    DROP CONSTRAINT IF EXISTS meetings_agenda_len,
    DROP CONSTRAINT IF EXISTS meetings_notes_len;

ALTER TABLE transactions
    DROP CONSTRAINT IF EXISTS tx_currency_len,
    DROP CONSTRAINT IF EXISTS tx_provider_len,
    DROP CONSTRAINT IF EXISTS tx_notes_len;

ALTER TABLE dues_invoices
    DROP CONSTRAINT IF EXISTS dues_period_len,
    DROP CONSTRAINT IF EXISTS dues_currency_len,
    DROP CONSTRAINT IF EXISTS dues_notes_len;

ALTER TABLE members
    DROP CONSTRAINT IF EXISTS members_display_name_len,
    DROP CONSTRAINT IF EXISTS members_email_len,
    DROP CONSTRAINT IF EXISTS members_phone_len,
    DROP CONSTRAINT IF EXISTS members_address_len,
    DROP CONSTRAINT IF EXISTS members_tier_len,
    DROP CONSTRAINT IF EXISTS members_notes_len;

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_email_len;
