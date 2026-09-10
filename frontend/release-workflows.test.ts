import { readdir, readFile, stat } from "node:fs/promises";
import path from "node:path";
import { describe, expect, it } from "vitest";

const repositoryRoot = path.resolve(import.meta.dirname, "..");
const releaseMutationPatterns = [
  /gh release (?:create|delete|edit|publish|upload)\b/,
  /gh api(?=[^\n]*(?:releases|git\/refs\/tags))(?=[^\n]*(?:(?:--method|-X)\s+(?:DELETE|PATCH|POST|PUT)))[^\n]*/,
  /curl(?=[^\n]*(?:releases|git\/refs\/tags))(?=[^\n]*(?:(?:--request|-X)\s*(?:DELETE|PATCH|POST|PUT)))[^\n]*/,
  /github\.rest\.repos\.(?:createRelease|deleteRelease|updateRelease|uploadReleaseAsset)\b/,
  /uses:\s*(?:actions\/create-release|ncipollo\/release-action|softprops\/action-gh-release)@/,
  /git tag\b(?![^\n]*(?:--list\b|-l\b|--contains\b|--no-contains\b|--points-at\b|--merged\b|--no-merged\b|--sort\b|--column\b|--ignore-case\b|--verify\b|-v\b|-n\d*\b))[^\n]*\S/,
  /git push\b[^\n]*(?:--tags\b|--follow-tags\b|refs\/tags\/|\btag\s+\S+|\bv?\d+\.\d+\.\d+(?:[-+][^\s]+)?\b)/,
  /electron-forge publish/,
  /npm run publish/,
];

type WorkflowSource = { name: string; contents: string };

function findReleaseMutationViolations(workflows: WorkflowSource[]) {
  return workflows.flatMap(({ name, contents }) => {
    const logicalCommands = contents.replace(/\\\r?\n[ \t]*/g, " ");
    const violations = releaseMutationPatterns
      .filter((pattern) => pattern.test(logicalCommands))
      .map((pattern) => `${name}: ${pattern.source}`);

    if (
      /contents:\s*write/.test(contents) &&
      /(?:\brelease\b|git\/refs\/tags)/i.test(contents)
    ) {
      violations.push(`${name}: write-enabled release workflow`);
    }

    return violations;
  });
}

describe("desktop release workflows", () => {
  const workflowsDirectory = path.join(repositoryRoot, ".github", "workflows");
  const artifactBuilder = path.join(workflowsDirectory, "build-artifacts.yml");

  async function readWorkflows() {
    const names = (await readdir(workflowsDirectory)).filter((name) =>
      /\.ya?ml$/.test(name),
    );
    return Promise.all(
      names.map(async (name) => ({
        name,
        contents: await readFile(path.join(workflowsDirectory, name), "utf8"),
      })),
    );
  }

  it("removes obsolete public release mutation workflows", async () => {
    for (const name of [
      "frontend-release.yml",
      "feature-release-cleanup.yml",
    ]) {
      await expect(
        stat(path.join(workflowsDirectory, name)),
      ).rejects.toMatchObject({ code: "ENOENT" });
    }
  });

  it("prevents a public desktop tag publisher from returning", async () => {
    const workflows = await readWorkflows();

    expect(workflows.map(({ contents }) => contents).join("\n")).not.toContain(
      "desktop-v*",
    );
  });

  it("prevents public workflows from mutating releases or release tags", async () => {
    const workflows = await readWorkflows();

    expect(findReleaseMutationViolations(workflows)).toEqual([]);
  });

  it("recognizes multiline mutations without rejecting allowed workflows", () => {
    const forbidden = [
      {
        name: "multiline-gh-api.yml",
        contents: String.raw`gh api \
          --method DELETE \
          repos/o/r/releases/123`,
      },
      {
        name: "multiline-curl.yml",
        contents: String.raw`curl \
          -X POST \
          https://api.github.com/repos/o/r/releases`,
      },
      {
        name: "write-enabled-release.yml",
        contents: "name: Release publisher\npermissions:\n  contents: write\n",
      },
      {
        name: "direct-tag-publisher.yml",
        contents:
          "permissions: { contents: write }\nrun: git tag v1.2.3 && git push origin v1.2.3\n",
      },
      {
        name: "tag-ref-publisher.yml",
        contents:
          "permissions: { contents: write }\nrun: git push origin HEAD:refs/tags/v1.2.3\n",
      },
      {
        name: "annotated-tag-publisher.yml",
        contents:
          'permissions: { contents: write }\nrun: git tag -m "release" v1.2.3 && git push origin --follow-tags\n',
      },
      {
        name: "combined-annotated-tag-publisher.yml",
        contents:
          'permissions: { contents: write }\nrun: git tag -am "release" v1.2.3 && git push origin --follow-tags\n',
      },
      {
        name: "flow-write-release.yml",
        contents: "name: Release publisher\npermissions: { contents: write }\n",
      },
    ];
    const allowed = [
      {
        name: "release-monitor.yml",
        contents: String.raw`permissions:
  contents: read
run: |
  gh api \
    repos/o/r/releases/latest
  curl \
    -X GET \
    https://api.github.com/repos/o/r/releases/latest
  gh release view v1.2.3`,
      },
      {
        name: "unrelated-write.yml",
        contents:
          "name: Label issue\npermissions:\n  contents: write\nrun: gh api -X POST repos/o/r/issues/1/labels\n",
      },
      {
        name: "unrelated-git-push.yml",
        contents:
          "permissions: { contents: write }\nrun: |\n  git tag --list\n  git push origin HEAD:automation-results\n",
      },
    ];

    for (const workflow of forbidden) {
      expect(findReleaseMutationViolations([workflow])).not.toEqual([]);
    }
    expect(findReleaseMutationViolations(allowed)).toEqual([]);
  });

  it("keeps the conductor artifact builder dispatchable and read-only", async () => {
    const contents = await readFile(artifactBuilder, "utf8");

    expect(contents).toContain("workflow_dispatch:");
    expect(contents).toMatch(/permissions:\s*\n\s*contents:\s*read/);
    expect(contents).not.toContain("contents: write");
    expect(contents).not.toContain("${{ secrets.");
    expect(contents).not.toMatch(
      /(?:gh release|electron-forge publish|npm run publish)/,
    );
  });

  it("requires the WorkOS client ID for unsigned artifacts", async () => {
    const contents = await readFile(artifactBuilder, "utf8");

    expect(contents).toContain(
      "VITE_WORKOS_CLIENT_ID: ${{ vars.VITE_WORKOS_CLIENT_ID }}",
    );
    expect(contents).toContain(
      "Repository variable VITE_WORKOS_CLIENT_ID is required",
    );
  });
});
