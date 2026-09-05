import type { UpdateStatus } from "./update-settings";
import type { StagedUpdateJournal, UpdateCandidate, UpdateChannelIdentity } from "./staged-update-state";

export interface UpdaterCandidateInput {
  version: string;
  channel: UpdateChannelIdentity;
  operationId: string;
  releaseTag?: string;
  feedUrl?: string;
  assetName?: string;
  sha512?: string;
}

export function candidateFromUpdateInfo(input: UpdaterCandidateInput): UpdateCandidate {
  return {
    ...input,
    ...(input.channel.startsWith("pr") ? { featurePr: Number(input.channel.slice(2)) } : {}),
  };
}

export interface ReplacementProgress {
  percent?: number;
  transferred?: number;
  total?: number;
  bytesPerSecond?: number;
}

export function journalToUpdateStatus(journal: StagedUpdateJournal, progress: ReplacementProgress = {}): UpdateStatus {
  if (journal.state === "native-possibly-staged") {
    return { state: "downloaded", version: journal.staged.version, stagedAt: journal.stagedAt, stagedCandidate: journal.staged };
  }
  if (journal.state === "replacing" || journal.state === "replacement-failed") {
    const etaSeconds = progress.bytesPerSecond && progress.total !== undefined && progress.transferred !== undefined
      ? Math.max(0, Math.ceil((progress.total - progress.transferred) / progress.bytesPerSecond))
      : undefined;
    return {
      state: journal.state,
      version: journal.replacement.version,
      ...(journal.state === "replacement-failed" ? { message: journal.message } : {
        percent: progress.percent,
        transferred: progress.transferred,
        total: progress.total,
        bytesPerSecond: progress.bytesPerSecond,
      }),
      ...(etaSeconds === undefined ? {} : { etaSeconds }),
      stagedCandidate: journal.staged,
      replacementCandidate: journal.replacement,
      nativeCandidates: journal.nativeCandidates,
      replacementPhase: journal.phase,
      installDisabledReason: `Replacement ${journal.replacement.version} is incomplete. Quitting may still install ${journal.staged.version}.${journal.nativeCandidates?.length ? ` Earlier native candidates remain unverified: ${journal.nativeCandidates.map((candidate) => candidate.version).join(", ")}.` : ""}`,
    };
  }
  if (journal.state === "version-mismatch") {
    return { state: "checking", stagedCandidate: journal.staged, message: `Running ${journal.runningVersion}; checking for the expected update after restart.` };
  }
  return { state: "idle" };
}

export function effectiveUpdateChannel(channel: string | null | undefined): UpdateChannelIdentity {
  if (channel === "nightly" || (typeof channel === "string" && /^pr[1-9]\d*$/.test(channel))) return channel as UpdateChannelIdentity;
  return "latest";
}
