-- +goose Up
-- Existing projects explicitly retain origin-only claim trust. Do not discover
-- remotes or infer an upstream during upgrade. NULL configs still mean defaults.
UPDATE projects
SET config = json_set(config, '$.canonicalRepoURL', '')
WHERE config IS NOT NULL AND json_valid(config)
  AND json_type(config, '$.canonicalRepoURL') IS NULL;

-- +goose Down
-- Preserve explicit trust configuration on downgrade, like other project settings.
