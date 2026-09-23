-- +goose Up
-- Intermediate PR #4028 builds applied 0130 with DEFAULT FALSE, before stored
-- review completeness was reliable. They already have agent_install_jobs and
-- review_partial, so neither the legacy-0123 repair nor the corrected 0130 runs.
-- The old column default identifies these databases. Invalidate their certainty
-- once, without discarding review rows or observation timestamps. A successful
-- full review fetch can restore an exact count, which survives later startups
-- because Goose records this data migration as applied.
-- Databases created with the conservative DEFAULT TRUE keep valid observations.
UPDATE pr
SET review_partial = TRUE
WHERE review_partial = FALSE
  AND EXISTS (
    SELECT 1 FROM pragma_table_info('pr')
    WHERE name = 'review_partial' AND upper(dflt_value) = 'FALSE'
  );

-- +goose Down
-- Deliberately irreversible: an older completeness claim cannot be recovered
-- safely without a successful full review fetch.
