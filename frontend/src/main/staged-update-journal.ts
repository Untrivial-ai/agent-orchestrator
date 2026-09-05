import { copyFile, mkdir, open, readFile, rename } from "node:fs/promises";
import path from "node:path";
import { transitionStagedUpdate, type ReplacementPhase, type StagedUpdateJournal, type UpdateCandidate } from "./staged-update-state";

export const STAGED_UPDATE_JOURNAL_FILE = "staged-update-journal.json";
const phases = new Set<ReplacementPhase>(["checking", "differential", "full-fallback", "verifying", "native-handoff"]);

function isCandidate(value: unknown): value is UpdateCandidate {
  if (value === null || typeof value !== "object") return false;
  const candidate = value as Record<string, unknown>;
  const channel = candidate.channel;
  return typeof candidate.version === "string" && candidate.version.length > 0
    && typeof candidate.operationId === "string" && candidate.operationId.length > 0
    && (channel === "latest" || channel === "nightly" || (typeof channel === "string" && /^pr[1-9]\d*$/.test(channel)))
    && (candidate.featurePr === undefined || (Number.isInteger(candidate.featurePr) && (candidate.featurePr as number) > 0))
    && ["releaseTag", "feedUrl", "assetName", "sha512"].every((key) => candidate[key] === undefined || typeof candidate[key] === "string");
}

function isTimestamp(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value) && value > 0;
}

function parseJournal(value: unknown): StagedUpdateJournal {
  if (value === null || typeof value !== "object") throw new Error("invalid staged update journal");
  const journal = value as Record<string, unknown>;
  if (journal.schemaVersion !== 1 || typeof journal.state !== "string") throw new Error("invalid staged update journal schema");
  if (journal.state === "none") return { schemaVersion: 1, state: "none" };
  if (journal.nativeCandidates !== undefined && (!Array.isArray(journal.nativeCandidates) || !journal.nativeCandidates.every(isCandidate))) throw new Error("invalid native candidate history");
  if (!isCandidate(journal.staged)) throw new Error("invalid staged update candidate");
  if (journal.state === "native-possibly-staged" && isTimestamp(journal.stagedAt)) return journal as StagedUpdateJournal;
  if ((journal.state === "replacing" || journal.state === "replacement-failed") && isCandidate(journal.replacement) && phases.has(journal.phase as ReplacementPhase)) {
    if (journal.state === "replacing" && isTimestamp(journal.startedAt)) return journal as StagedUpdateJournal;
    if (journal.state === "replacement-failed" && isTimestamp(journal.failedAt) && typeof journal.message === "string") return journal as StagedUpdateJournal;
  }
  if (journal.state === "version-mismatch" && typeof journal.runningVersion === "string" && isTimestamp(journal.detectedAt)) return journal as StagedUpdateJournal;
  throw new Error("invalid staged update journal state");
}

export class StagedUpdateJournalStore {
  private queue: Promise<void> = Promise.resolve();
  private readonly file: string;

  constructor(private readonly stateDir: string) {
    this.file = path.join(stateDir, STAGED_UPDATE_JOURNAL_FILE);
  }

  write(journal: StagedUpdateJournal): Promise<void> {
    const operation = this.queue.then(() => this.writeAtomic(journal));
    this.queue = operation.catch(() => undefined);
    return operation;
  }

  async read(runningVersion: string): Promise<StagedUpdateJournal> {
    await this.queue;
    let raw: string;
    try {
      raw = await readFile(this.file, "utf8");
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === "ENOENT") {
        // #4849 persisted this unversioned record. Preserve its native provenance
        // when upgrading to the journal, including after a non-installing quit.
        let legacy: { version?: unknown; channel?: unknown; stagedAt?: unknown };
        try { legacy = JSON.parse(await readFile(path.join(this.stateDir, "staged-update.json"), "utf8")); }
        catch (legacyError) {
          if ((legacyError as NodeJS.ErrnoException).code === "ENOENT") return { schemaVersion: 1, state: "none" };
          throw legacyError;
        }
        const candidate = { version: legacy.version, channel: legacy.channel, operationId: "legacy-migration" };
        if (!isCandidate(candidate) || !isTimestamp(legacy.stagedAt)) throw new Error("invalid legacy staged update provenance");
        return transitionStagedUpdate({ schemaVersion: 1, state: "native-possibly-staged", staged: candidate, stagedAt: legacy.stagedAt }, { type: "reconcile-running-version", version: runningVersion });
      }
      throw error;
    }
    try {
      const journal = parseJournal(JSON.parse(raw));
      return transitionStagedUpdate(journal, { type: "reconcile-running-version", version: runningVersion });
    } catch (error) {
      const quarantine = `${this.file}.corrupt-${Date.now()}`;
      await copyFile(this.file, quarantine);
      throw new Error(`invalid staged update journal quarantined at ${quarantine}`, { cause: error });
    }
  }

  private async writeAtomic(journal: StagedUpdateJournal): Promise<void> {
    await mkdir(this.stateDir, { recursive: true, mode: 0o750 });
    const temp = `${this.file}.tmp-${process.pid}-${crypto.randomUUID()}`;
    const handle = await open(temp, "wx", 0o600);
    try {
      await handle.writeFile(`${JSON.stringify(journal)}\n`, "utf8");
      await handle.sync();
    } finally {
      await handle.close();
    }
    await rename(temp, this.file);
    // Windows cannot flush a directory opened with Node's read-only handle.
    // The file was flushed before the atomic rename; directory fsync is POSIX-only.
    if (process.platform === "win32") return;
    const directory = await open(this.stateDir, "r");
    try {
      await directory.sync();
    } finally {
      await directory.close();
    }
  }
}
