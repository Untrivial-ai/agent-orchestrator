-- +goose Up

-- Allow one GitHub App installation to be connected by multiple AO orgs.
--
-- Previously ao_github_installations was globally UNIQUE (github_installation_id),
-- so once one org connected an installation no other org could — a second
-- verified GitHub admin of the same installation hit a hard conflict. Relax the
-- uniqueness to (org_id, github_installation_id) so each org gets its own
-- installation record (isolated AO sessions/projects/repository grants) while
-- sharing the same underlying GitHub installation. GitHub App installation
-- access tokens are minted per installation_id and are org-agnostic on GitHub's
-- side, so multiple AO orgs minting for the same installation_id is safe; the
-- per-org repository grants still scope what each org can see.
ALTER TABLE ao_github_installations
    DROP CONSTRAINT ao_github_installations_github_installation_id_key,
    ADD CONSTRAINT ao_github_installations_org_github_installation_key
        UNIQUE (org_id, github_installation_id);

-- The webhook route table must likewise hold one row per (installation, org) so
-- a single GitHub webhook delivery fans out to every org that connected the
-- installation. Widen its primary key from github_installation_id alone to
-- (github_installation_id, org_id). The existing composite foreign key to
-- ao_github_installations(org_id, id) is unaffected.
ALTER TABLE ao_github_installation_routes
    DROP CONSTRAINT ao_github_installation_routes_pkey,
    ADD CONSTRAINT ao_github_installation_routes_pkey
        PRIMARY KEY (github_installation_id, org_id);

-- +goose Down

-- Re-tightening to a single org per installation is only lossless while no
-- installation has been connected by more than one org. If duplicates exist,
-- restoring the global-unique/primary-key constraints will fail loudly rather
-- than silently discarding a tenant's installation — which is the correct,
-- non-destructive behavior for an intentionally one-way relaxation.
ALTER TABLE ao_github_installation_routes
    DROP CONSTRAINT ao_github_installation_routes_pkey,
    ADD CONSTRAINT ao_github_installation_routes_pkey
        PRIMARY KEY (github_installation_id);

ALTER TABLE ao_github_installations
    DROP CONSTRAINT ao_github_installations_org_github_installation_key,
    ADD CONSTRAINT ao_github_installations_github_installation_id_key
        UNIQUE (github_installation_id);
