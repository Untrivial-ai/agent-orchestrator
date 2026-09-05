export type UpdateChannelIdentity = "latest" | "nightly" | `pr${number}`;

export interface UpdateCandidate {
  version: string;
  channel: UpdateChannelIdentity;
  featurePr?: number;
  releaseTag?: string;
  feedUrl?: string;
  assetName?: string;
  sha512?: string;
  operationId: string;
}

export type ReplacementPhase =
  | "checking"
  | "differential"
  | "full-fallback"
  | "verifying"
  | "native-handoff";

export type StagedUpdateJournal =
  | { schemaVersion: 1; state: "none" }
  | { schemaVersion: 1; state: "native-possibly-staged"; staged: UpdateCandidate; stagedAt: number }
  | { schemaVersion: 1; state: "replacing"; nativeCandidates?: UpdateCandidate[]; stagedAt?: number; runningVersion?: string; staged: UpdateCandidate; replacement: UpdateCandidate; startedAt: number; phase: ReplacementPhase }
  | { schemaVersion: 1; state: "replacement-failed"; nativeCandidates?: UpdateCandidate[]; stagedAt?: number; runningVersion?: string; staged: UpdateCandidate; replacement: UpdateCandidate; failedAt: number; phase: ReplacementPhase; message: string }
  | { schemaVersion: 1; state: "version-mismatch"; stagedAt?: number; staged: UpdateCandidate; runningVersion: string; detectedAt: number };

export type StagedUpdateEvent =
  | { type: "replacement-discovered"; replacement: UpdateCandidate; at: number }
  | { type: "replacement-phase"; operationId: string; phase: ReplacementPhase }
  | { type: "replacement-failed"; operationId: string; at: number; message: string }
  | { type: "handoff-succeeded"; operationId: string; at: number }
  | { type: "initial-handoff-succeeded"; candidate: UpdateCandidate; at: number }
  | { type: "no-update" }
  | { type: "reconcile-running-version"; version: string; at?: number };

function activeReplacement(state: StagedUpdateJournal): UpdateCandidate | undefined {
  return state.state === "replacing" || state.state === "replacement-failed"
    ? state.replacement
    : undefined;
}

function requireOperation(state: StagedUpdateJournal, operationId: string): UpdateCandidate {
  const replacement = activeReplacement(state);
  if (replacement?.operationId !== operationId) {
    throw new Error(`stale or illegal staged-update operation: ${operationId}`);
  }
  return replacement;
}

export function transitionStagedUpdate(
  state: StagedUpdateJournal,
  event: StagedUpdateEvent,
): StagedUpdateJournal {
  switch (event.type) {
    case "replacement-discovered": {
      if (state.state === "none") {
        throw new Error("replacement discovery requires a possibly staged candidate");
      }
      const staged = state.staged;
      const previous = activeReplacement(state);
      const nativeCandidates = state.state === "replacing" || state.state === "replacement-failed"
        ? [...(state.nativeCandidates ?? [])]
        : [];
      if (previous && (state.state === "replacing" || state.state === "replacement-failed") &&
        (state.phase === "verifying" || state.phase === "native-handoff") &&
        !nativeCandidates.some((candidate) => candidate.operationId === previous.operationId)) {
        nativeCandidates.push(previous);
      }
      const returnsToStaged = event.replacement.version === staged.version && event.replacement.channel === staged.channel && event.replacement.sha512 === staged.sha512 && event.replacement.assetName === staged.assetName;
      if (returnsToStaged && nativeCandidates.length === 0) {
        if (!previous) return state;
        return { schemaVersion: 1, state: "native-possibly-staged", staged, stagedAt: state.stagedAt ?? event.at };
      }
      return { schemaVersion: 1, state: "replacing", staged, stagedAt: state.stagedAt, ...(nativeCandidates.length ? { nativeCandidates } : {}), replacement: event.replacement, startedAt: event.at, phase: "checking" };
    }
    case "replacement-phase": {
      const replacement = requireOperation(state, event.operationId);
      if (state.state !== "replacing") throw new Error("replacement phase requires an active replacement");
      return { ...state, replacement, phase: event.phase };
    }
    case "replacement-failed": {
      const replacement = requireOperation(state, event.operationId);
      if (state.state !== "replacing") throw new Error("replacement failure requires an active replacement");
      return { schemaVersion: 1, state: "replacement-failed", staged: state.staged, stagedAt: state.stagedAt, nativeCandidates: state.nativeCandidates, replacement, failedAt: event.at, phase: state.phase, message: event.message };
    }
    case "handoff-succeeded": {
      const replacement = requireOperation(state, event.operationId);
      if (state.state !== "replacing" || state.phase !== "native-handoff") {
        throw new Error("handoff success requires the native-handoff phase");
      }
      return { schemaVersion: 1, state: "native-possibly-staged", staged: replacement, stagedAt: event.at };
    }
    case "initial-handoff-succeeded":
      if (state.state !== "none" && state.state !== "version-mismatch") throw new Error("initial handoff requires no tracked staged candidate");
      return { schemaVersion: 1, state: "native-possibly-staged", staged: event.candidate, stagedAt: event.at };
    case "no-update":
      return state;
    case "reconcile-running-version": {
      if (state.state === "none") return state;
      const expected = activeReplacement(state) ?? state.staged;
      if (event.version === expected.version) return { schemaVersion: 1, state: "none" };
      if (state.state === "replacing" || state.state === "replacement-failed") return event.version === state.staged.version ? state : { ...state, runningVersion: event.version };
      return { schemaVersion: 1, state: "version-mismatch", staged: state.staged, stagedAt: state.stagedAt, runningVersion: event.version, detectedAt: event.at ?? Date.now() };
    }
  }
}
