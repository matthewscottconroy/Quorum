-- Bound stored text lengths so an authenticated writer cannot persist
-- megabyte-scale values (the 1 MiB request cap alone allows large single
-- fields). Limits are generous — well above any legitimate value — and apply
-- uniformly to every write path, including direct SQL.

ALTER TABLE users
    ADD CONSTRAINT users_email_len CHECK (char_length(email) <= 320);

ALTER TABLE members
    ADD CONSTRAINT members_display_name_len CHECK (char_length(display_name) <= 300),
    ADD CONSTRAINT members_email_len   CHECK (email   IS NULL OR char_length(email)   <= 320),
    ADD CONSTRAINT members_phone_len   CHECK (phone   IS NULL OR char_length(phone)   <= 64),
    ADD CONSTRAINT members_address_len CHECK (address IS NULL OR char_length(address) <= 1000),
    ADD CONSTRAINT members_tier_len    CHECK (char_length(tier) <= 64),
    ADD CONSTRAINT members_notes_len   CHECK (notes   IS NULL OR char_length(notes)   <= 50000);

ALTER TABLE dues_invoices
    ADD CONSTRAINT dues_period_len   CHECK (char_length(period_label) <= 200),
    ADD CONSTRAINT dues_currency_len CHECK (char_length(currency) <= 8),
    ADD CONSTRAINT dues_notes_len    CHECK (notes IS NULL OR char_length(notes) <= 50000);

ALTER TABLE transactions
    ADD CONSTRAINT tx_currency_len CHECK (char_length(currency) <= 8),
    ADD CONSTRAINT tx_provider_len CHECK (char_length(provider) <= 32),
    ADD CONSTRAINT tx_notes_len    CHECK (notes IS NULL OR char_length(notes) <= 50000);

ALTER TABLE meetings
    ADD CONSTRAINT meetings_title_len    CHECK (char_length(title) <= 300),
    ADD CONSTRAINT meetings_location_len CHECK (location IS NULL OR char_length(location) <= 300),
    ADD CONSTRAINT meetings_agenda_len   CHECK (agenda   IS NULL OR char_length(agenda)   <= 50000),
    ADD CONSTRAINT meetings_notes_len    CHECK (notes    IS NULL OR char_length(notes)    <= 50000);

ALTER TABLE meeting_decisions
    ADD CONSTRAINT mdec_summary_len CHECK (char_length(summary) <= 1000),
    ADD CONSTRAINT mdec_detail_len  CHECK (detail IS NULL OR char_length(detail) <= 50000);

ALTER TABLE action_items
    ADD CONSTRAINT ai_title_len CHECK (char_length(title) <= 300),
    ADD CONSTRAINT ai_desc_len  CHECK (description IS NULL OR char_length(description) <= 50000);

ALTER TABLE plans
    ADD CONSTRAINT plans_title_len CHECK (char_length(title) <= 300),
    ADD CONSTRAINT plans_desc_len  CHECK (description IS NULL OR char_length(description) <= 50000);

ALTER TABLE plan_decisions
    ADD CONSTRAINT pdec_summary_len   CHECK (char_length(summary) <= 1000),
    ADD CONSTRAINT pdec_rationale_len CHECK (rationale IS NULL OR char_length(rationale) <= 50000);

ALTER TABLE contacts
    ADD CONSTRAINT contacts_name_len  CHECK (char_length(name) <= 300),
    ADD CONSTRAINT contacts_org_len   CHECK (organization IS NULL OR char_length(organization) <= 300),
    ADD CONSTRAINT contacts_email_len CHECK (email   IS NULL OR char_length(email)   <= 320),
    ADD CONSTRAINT contacts_phone_len CHECK (phone   IS NULL OR char_length(phone)   <= 64),
    ADD CONSTRAINT contacts_addr_len  CHECK (address IS NULL OR char_length(address) <= 1000),
    ADD CONSTRAINT contacts_cat_len   CHECK (category IS NULL OR char_length(category) <= 64),
    ADD CONSTRAINT contacts_notes_len CHECK (notes   IS NULL OR char_length(notes)   <= 50000);

ALTER TABLE resources
    ADD CONSTRAINT resources_title_len CHECK (char_length(title) <= 300),
    ADD CONSTRAINT resources_desc_len  CHECK (description IS NULL OR char_length(description) <= 50000),
    ADD CONSTRAINT resources_url_len   CHECK (url      IS NULL OR char_length(url)      <= 2000),
    ADD CONSTRAINT resources_cat_len   CHECK (category IS NULL OR char_length(category) <= 64);
