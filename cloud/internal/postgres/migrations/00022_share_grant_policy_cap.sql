-- +goose Up

-- ao_project_share_links already carries mode_cap/denied_commands, but
-- nothing copied them onto the grant a redemption creates, so they had no
-- runtime effect. Freezing them onto the grant at redemption time — the
-- same way role already is — means revoking or editing a link later
-- doesn't retroactively change an already-redeemed collaborator's policy,
-- consistent with how link revocation already doesn't touch existing
-- grants.
ALTER TABLE ao_project_share_grants
    ADD COLUMN mode_cap TEXT CHECK (mode_cap IN ('read-only', 'standard', 'trusted')),
    ADD COLUMN denied_commands TEXT[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE ao_project_share_grants
    DROP COLUMN mode_cap,
    DROP COLUMN denied_commands;
