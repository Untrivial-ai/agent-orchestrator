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
	return <CloudFileContent annotation={annotation} baseUrl={baseUrl} client={client} commitSha={commitSha} initialEditing={initialEditing} initialMode={initialMode} initialRequestKey={initialRequestKey} onDirtyChange={onDirtyChange ? (dirty) => onDirtyChange(path, dirty) : undefined} orgId={session.cloud?.orgId ?? ""} path={path} scope={scope} sessionId={session.id} split={split} />;
}
