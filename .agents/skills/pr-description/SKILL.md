---
name: pr-description
description: Calculate and maintain the required change-count header when creating or updating pull requests in this repository.
---

# PR description

Begin every PR description with these three lines, using Markdown hard line breaks
so each renders separately:

```text
Code changes: +<added> -<deleted>
Tests: +<added> -<deleted>
Others: +<added> -<deleted>
```

Calculate the numbers from the net diff against the PR's actual base. For a local
branch, use `git diff --numstat <base>...<head>`. After pushing, prefer GitHub's
per-file additions and deletions. Recalculate after the final push rather than
summing commit churn or estimating counts.

Count every changed file exactly once:

- **Code changes:** handwritten implementation, including SQL, migrations, styles,
  and executable scripts.
- **Tests:** test code, fixtures, snapshots, and test-only helpers.
- **Others:** generated or vendored files, lockfiles, documentation, configuration,
  and assets. Generated output belongs here even when it contains source code.

Show `+0 -0` for empty categories. Binary files have no line count; mention their
count separately when present. Add a brief qualifier when useful, such as
`Others: +11 -0 (generated sqlc)`.

Preserve the rest of an existing PR description. After creating or updating the PR,
read the published description and verify that its opening lines match the current
GitHub diff counts and required order.
