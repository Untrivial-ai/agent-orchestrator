-- +goose Up
-- Project registration wrote registered_at/archived_at from a bare time.Now(),
-- and the driver stored time.Time.String() in the local zone. In zones with a
-- numeric abbreviation that string is unparseable
-- ("2026-09-11 20:39:10.511227327 +0530 +0530 m=+73.186830353"), so the Scan in
-- ListProjects failed and releases with boot-time scratch seeding exited 1 on
-- every start. The connection now pins _timezone=UTC; this repairs databases
-- that already hold a tainted value.
--
-- The instant is preserved: the stored offset is subtracted to reach UTC and
-- the row is rewritten in the layout the UTC writer produces. Rows already in
-- "+0000 UTC" are left untouched, as are values with no parseable offset.

UPDATE projects AS t
SET registered_at = strftime('%Y-%m-%d %H:%M:%S', p.stamp,
                             p.inv || p.hh || ' hours',
                             p.inv || p.mm || ' minutes')
                    || p.frac || ' +0000 UTC'
FROM (
  SELECT id,
         substr(registered_at, 1, 19) AS stamp,
         CASE WHEN substr(registered_at, 20, 1) = '.'
              THEN substr(registered_at, 20, instr(substr(registered_at, 20) || ' ', ' ') - 1)
              ELSE '' END AS frac,
         CASE WHEN substr(registered_at, 21 + length(CASE WHEN substr(registered_at, 20, 1) = '.'
              THEN substr(registered_at, 20, instr(substr(registered_at, 20) || ' ', ' ') - 1)
              ELSE '' END), 1) = '+' THEN '-' ELSE '+' END AS inv,
         substr(registered_at, 22 + length(CASE WHEN substr(registered_at, 20, 1) = '.'
              THEN substr(registered_at, 20, instr(substr(registered_at, 20) || ' ', ' ') - 1)
              ELSE '' END), 2) AS hh,
         substr(registered_at, 24 + length(CASE WHEN substr(registered_at, 20, 1) = '.'
              THEN substr(registered_at, 20, instr(substr(registered_at, 20) || ' ', ' ') - 1)
              ELSE '' END), 2) AS mm
  FROM projects
  WHERE registered_at IS NOT NULL
    AND registered_at NOT LIKE '% +0000 UTC'
) AS p
WHERE t.id = p.id
  AND (p.inv = '-' OR p.inv = '+')
  AND p.hh GLOB '[0-9][0-9]'
  AND p.mm GLOB '[0-9][0-9]';

UPDATE projects AS t
SET archived_at = strftime('%Y-%m-%d %H:%M:%S', p.stamp,
                           p.inv || p.hh || ' hours',
                           p.inv || p.mm || ' minutes')
                  || p.frac || ' +0000 UTC'
FROM (
  SELECT id,
         substr(archived_at, 1, 19) AS stamp,
         CASE WHEN substr(archived_at, 20, 1) = '.'
              THEN substr(archived_at, 20, instr(substr(archived_at, 20) || ' ', ' ') - 1)
              ELSE '' END AS frac,
         CASE WHEN substr(archived_at, 21 + length(CASE WHEN substr(archived_at, 20, 1) = '.'
              THEN substr(archived_at, 20, instr(substr(archived_at, 20) || ' ', ' ') - 1)
              ELSE '' END), 1) = '+' THEN '-' ELSE '+' END AS inv,
         substr(archived_at, 22 + length(CASE WHEN substr(archived_at, 20, 1) = '.'
              THEN substr(archived_at, 20, instr(substr(archived_at, 20) || ' ', ' ') - 1)
              ELSE '' END), 2) AS hh,
         substr(archived_at, 24 + length(CASE WHEN substr(archived_at, 20, 1) = '.'
              THEN substr(archived_at, 20, instr(substr(archived_at, 20) || ' ', ' ') - 1)
              ELSE '' END), 2) AS mm
  FROM projects
  WHERE archived_at IS NOT NULL
    AND archived_at NOT LIKE '% +0000 UTC'
) AS p
WHERE t.id = p.id
  AND (p.inv = '-' OR p.inv = '+')
  AND p.hh GLOB '[0-9][0-9]'
  AND p.mm GLOB '[0-9][0-9]';

-- +goose Down
-- Deliberately irreversible: the original local zone is not recoverable from
-- the normalized value, and restoring it would re-break the Scan.
