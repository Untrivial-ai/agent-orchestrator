-- +goose Up
-- Rewrite sessions.activity_last_at values that a local-zone clock left in the
-- column. The driver stores a time.Time by its String() form, so a value written
-- from time.Now() (rather than time.Now().UTC()) kept both its zone and its
-- monotonic reading:
--
--   2026-06-28 18:45:08.349363 +0800 CST m=+25660.013723251
--
-- against the form every other writer produces:
--
--   2026-06-28 10:45:08.349363 +0000 UTC
--
-- activity_last_at is compared directly in SQL, and those comparisons are
-- lexicographic. A "+0800" wall clock sorts above the UTC rendering of a LATER
-- instant, so the agent-switch source-stop predicate
-- (activity_last_at <= stopped_at) silently matched zero rows: the confirmation
-- reported a phantom concurrent ownership change and stranded the saga in
-- stopping_source, where neither resume nor a repeat switch could recover it.
--
-- Shift each affected row back to UTC and re-render it in the canonical form.
-- Rows already ending in "+0000 UTC" are untouched.
--
-- Two shape details this has to respect:
--  * The offset is found by search, not at a fixed column: Go trims trailing
--    zeros from the fractional second, so ".42722 +0800" and ".349363 +0800"
--    place the sign differently.
--  * The fractional digits are carried over verbatim. strftime's %f renders
--    milliseconds only, which would silently truncate microsecond precision.

-- +goose StatementBegin
UPDATE sessions
SET activity_last_at =
    strftime('%Y-%m-%d %H:%M:%S',
        substr(activity_last_at, 1, instr(activity_last_at, ' ') + instr(substr(activity_last_at, instr(activity_last_at, ' ') + 1), ' ') - 1),
        (CASE substr(activity_last_at, instr(activity_last_at, ' ') + instr(substr(activity_last_at, instr(activity_last_at, ' ') + 1), ' ') + 1, 1)
            WHEN '+' THEN '-' ELSE '+' END)
            || substr(activity_last_at, instr(activity_last_at, ' ') + instr(substr(activity_last_at, instr(activity_last_at, ' ') + 1), ' ') + 2, 2) || ' hours',
        (CASE substr(activity_last_at, instr(activity_last_at, ' ') + instr(substr(activity_last_at, instr(activity_last_at, ' ') + 1), ' ') + 1, 1)
            WHEN '+' THEN '-' ELSE '+' END)
            || substr(activity_last_at, instr(activity_last_at, ' ') + instr(substr(activity_last_at, instr(activity_last_at, ' ') + 1), ' ') + 4, 2) || ' minutes'
    )
    || CASE
        WHEN instr(substr(activity_last_at, 1, instr(activity_last_at, ' ') + instr(substr(activity_last_at, instr(activity_last_at, ' ') + 1), ' ') - 1), '.') = 0
        THEN ''
        ELSE substr(
            substr(activity_last_at, 1, instr(activity_last_at, ' ') + instr(substr(activity_last_at, instr(activity_last_at, ' ') + 1), ' ') - 1),
            instr(substr(activity_last_at, 1, instr(activity_last_at, ' ') + instr(substr(activity_last_at, instr(activity_last_at, ' ') + 1), ' ') - 1), '.')
        )
    END
    || ' +0000 UTC'
-- Matched by "not already UTC" rather than by the monotonic suffix: a
-- local-zone value breaks the comparison whether or not it carried a monotonic
-- reading, and both shapes exist in the wild ("… +0700 +07 m=+995.1" and
-- "… +0700 +07").
WHERE activity_last_at NOT LIKE '% +0000 UTC'
  AND substr(activity_last_at, instr(activity_last_at, ' ') + instr(substr(activity_last_at, instr(activity_last_at, ' ') + 1), ' ') + 1, 1) IN ('+', '-')
  AND strftime('%Y-%m-%d %H:%M:%S',
        substr(activity_last_at, 1, instr(activity_last_at, ' ') + instr(substr(activity_last_at, instr(activity_last_at, ' ') + 1), ' ') - 1),
        '+00 hours'
      ) IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- Irreversible by design: the original strings carried a monotonic reading that
-- was never meaningful outside the process that wrote it, and the normalized
-- values are what every reader and comparison already expects.
SELECT 1;
