import { describe, expect, it } from "vitest";
import { candidateFromUpdateInfo, journalToUpdateStatus } from "./staged-update-adapter";
import type { UpdateStatus } from "./update-settings";
import type { StagedUpdateJournal } from "./staged-update-state";

describe("staged update adapter", () => {
  it.each([
    ["manual", "manual-1"],
    ["automatic", "automatic-1"],
    ["channel switch", "channel-1"],
    ["return home", "home-1"],
  ])("preserves %s operation identity", (_name, operationId) => {
    expect(candidateFromUpdateInfo({ version: "2.0.0", channel: "nightly", operationId })).toMatchObject({ version: "2.0.0", channel: "nightly", operationId });
  });

  it("derives feature PR provenance from its channel", () => {
    expect(candidateFromUpdateInfo({ version: "2.0.0-pr42.1", channel: "pr42", operationId: "feature-42", releaseTag: "v2.0.0-pr42.1", assetName: "ao.zip", sha512: "abc" })).toEqual({
      version: "2.0.0-pr42.1", channel: "pr42", featurePr: 42, operationId: "feature-42", releaseTag: "v2.0.0-pr42.1", assetName: "ao.zip", sha512: "abc",
    });
  });

  it("maps replacement progress without losing A or B", () => {
    const journal: StagedUpdateJournal = {
      schemaVersion: 1,
      state: "replacing",
      staged: { version: "1.0.0", channel: "latest", operationId: "a" },
      replacement: { version: "2.0.0", channel: "nightly", operationId: "b" },
      startedAt: 100,
      phase: "full-fallback",
    };
    expect(journalToUpdateStatus(journal, { percent: 23, transferred: 230, total: 1000, bytesPerSecond: 50 })).toMatchObject({
      state: "replacing",
      version: "2.0.0",
      percent: 23,
      stagedCandidate: journal.staged,
      replacementCandidate: journal.replacement,
      replacementPhase: "full-fallback",
      installDisabledReason: expect.stringContaining("incomplete"),
    });
  });

  it("maps failure to retryable B while warning that A remains staged", () => {
    const journal: StagedUpdateJournal = {
      schemaVersion: 1,
      state: "replacement-failed",
      staged: { version: "1.0.0", channel: "latest", operationId: "a" },
      replacement: { version: "2.0.0", channel: "nightly", operationId: "b" },
      failedAt: 200,
      phase: "verifying",
      message: "checksum mismatch",
    };
    expect(journalToUpdateStatus(journal)).toMatchObject({ state: "replacement-failed", message: "checksum mismatch", version: "2.0.0", stagedCandidate: journal.staged, replacementCandidate: journal.replacement });
  });
  it("does not let a status-shaped progress input override journal identity", () => {
    const journal: StagedUpdateJournal = { schemaVersion: 1, state: "replacing", staged: { version: "1", channel: "latest", operationId: "a" }, replacement: { version: "2", channel: "latest", operationId: "b" }, startedAt: 100, phase: "checking" };
    const progress: UpdateStatus = { state: "downloaded", version: "1", percent: 25 };
    expect(journalToUpdateStatus(journal, progress)).toMatchObject({ state: "replacing", version: "2", percent: 25 });
  });

});
