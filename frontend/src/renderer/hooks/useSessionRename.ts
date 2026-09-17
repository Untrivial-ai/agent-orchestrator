import { useCallback, useRef, useState } from "react";
import { renameSession } from "../lib/rename-session";
import type { WorkspaceSession } from "../types/workspace";

export const MAX_SESSION_DISPLAY_NAME_LEN = 20;

type RenameableSession = Pick<WorkspaceSession, "id" | "title">;

export function useSessionRename(session?: RenameableSession, onRenamed?: () => void | Promise<void>) {
  const [isEditing, setIsEditing] = useState(false);
  const [draft, setDraft] = useState(session?.title ?? "");
  const cancelledRef = useRef(false);

  const begin = useCallback(() => {
    if (!session) return;
    cancelledRef.current = false;
    setDraft(session.title);
    setIsEditing(true);
  }, [session]);

  const cancel = useCallback(() => {
    cancelledRef.current = true;
    setDraft(session?.title ?? "");
    setIsEditing(false);
  }, [session?.title]);

  const commit = useCallback(async () => {
    if (cancelledRef.current) {
      cancelledRef.current = false;
      setIsEditing(false);
      return;
    }
    setIsEditing(false);
    if (!session) return;
    const name = draft.trim();
    if (!name || name === session.title) return;
		try {
			await renameSession(session.id, name);
			await onRenamed?.();
		} catch (error) {
      console.error("Failed to rename session:", error);
    }
	}, [draft, onRenamed, session]);

  return { begin, cancel, commit, draft, isEditing, setDraft };
}
