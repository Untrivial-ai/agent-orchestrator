import { useCallback } from "react";
import { useCloudCp } from "../hooks/useCloudCp";
import type { CloudCpWorkspaceReviewScope } from "../lib/cloud-cp";
import type { WorkspaceSession } from "../types/workspace";
import type { FileAnnotationModel } from "./WorkspaceDiffView";
import { CloudFileContentPane as CloudFileContent } from "./cloud-workspace/CloudFileContentPane";
import type { CloudFileViewMode } from "./cloud-workspace/CloudFileContentPane";
import { CloudWorkspaceExplorer, type CloudFileOpenOptions } from "./cloud-workspace/CloudWorkspaceExplorer";

export function CloudWorkspaceDiff({ annotation, session, isMaximized = false, onOpenFile, onSplitChange, onToggleMaximized, split }: {
	annotation?: FileAnnotationModel;
	session: WorkspaceSession;
	isMaximized?: boolean;
	onOpenFile?: (path: string, options?: CloudFileOpenOptions) => void;
	onSplitChange?: (split: boolean) => void;
	onToggleMaximized?: (next: boolean) => void;
	split?: boolean;
}) {
	return <CloudWorkspaceExplorer annotation={annotation} isMaximized={isMaximized} onOpenFile={onOpenFile} onSplitChange={onSplitChange} onToggleMaximized={onToggleMaximized} session={session} split={split} />;
}

export function CloudFileContentPane({ annotation, commitSha, initialEditing, initialMode, initialRequestKey, onDirtyChange, path, scope, session, split = false }: {
	annotation: FileAnnotationModel;
	commitSha?: string;
	initialEditing?: boolean;
	initialMode?: CloudFileViewMode;
	initialRequestKey?: number;
	onDirtyChange?: (path: string, dirty: boolean) => void;
	path: string;
	scope?: CloudCpWorkspaceReviewScope;
	session: WorkspaceSession;
	split?: boolean;
}) {
	const { baseUrl, client } = useCloudCp();
	const handleDirtyChange = useCallback((dirty: boolean) => onDirtyChange?.(path, dirty), [onDirtyChange, path]);
	return <section className="relative flex h-full min-h-0 flex-col bg-background">
		<div className="board-scrollbar min-h-0 flex-1 overflow-x-hidden overflow-y-auto overscroll-contain">
			<CloudFileContent annotation={annotation} baseUrl={baseUrl} client={client} commitSha={commitSha} initialEditing={initialEditing} initialMode={initialMode} initialRequestKey={initialRequestKey} onDirtyChange={handleDirtyChange} orgId={session.cloud?.orgId ?? ""} path={path} scope={scope} sessionId={session.id} split={split} />
		</div>
	</section>;
}
