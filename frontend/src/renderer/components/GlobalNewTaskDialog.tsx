import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { editorHandoffQueryKey } from "../hooks/useEditorHandoff";
import { workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { useUiStore } from "../stores/ui-store";
import { NewTaskDialog } from "./NewTaskDialog";

// App-level New Task surface. Lives in the shell (always mounted, on every
// route and platform, unlike ShellTopbar which unmounts on Linux boards) so a
// ⌘N / Ctrl+Shift+N shortcut or the sidebar "New session" item can open a
// worker for any project — including one you are not currently viewing.
// Driven by the ui-store `newTaskRequest` signal; the nonce lets a repeat
// request for the same project re-open the dialog.
export function GlobalNewTaskDialog() {
	const navigate = useNavigate();
	const queryClient = useQueryClient();
	const newTaskRequest = useUiStore((state) => state.newTaskRequest);
	const [open, setOpen] = useState(false);
	const [projectId, setProjectId] = useState<string | undefined>(undefined);
	const lastNonce = useRef(0);

	useEffect(() => {
		if (!newTaskRequest || newTaskRequest.nonce === lastNonce.current) return;
		lastNonce.current = newTaskRequest.nonce;
		// Consume requests that arrive while this dialog is already open. In
		// particular, do not retarget a populated form to another project, and do
		// not replay the ignored request when the user later closes the dialog.
		if (open) return;
		setProjectId(newTaskRequest.projectId);
		setOpen(true);
	}, [newTaskRequest, open]);

	const handleCreated = async (sessionId: string) => {
		if (!projectId) return;
		await Promise.all([
			queryClient.invalidateQueries({ queryKey: workspaceQueryKey }),
			queryClient.invalidateQueries({ queryKey: editorHandoffQueryKey(sessionId) }),
		]);
		void navigate({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId, sessionId },
		});
	};

	return (
		<NewTaskDialog
			open={open}
			projectId={projectId}
			onCreated={(sessionId) => void handleCreated(sessionId)}
			onOpenChange={setOpen}
		/>
	);
}
