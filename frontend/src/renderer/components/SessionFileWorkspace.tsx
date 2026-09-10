import { FileContentPane } from "./FileContentPane";
import { FileAnnotationComposer, type FileAnnotationModel } from "./WorkspaceDiffView";

export function SessionFileWorkspace({
	annotation,
	path,
	sessionId,
	split,
}: {
	annotation: FileAnnotationModel;
	path: string;
	sessionId: string;
	split: boolean;
}) {
	const fileFeedbackActive = annotation.target?.path === path && annotation.target.side === "file";
	return (
		<section className="relative flex h-full min-h-0 flex-col bg-background" data-testid="session-file-workspace">
			{fileFeedbackActive ? (
				<div className="pointer-events-none absolute inset-x-4 top-4 z-30 flex justify-center">
					<div className="pointer-events-auto w-full max-w-xl">
						<FileAnnotationComposer annotation={annotation} />
					</div>
				</div>
			) : null}
			<div className="board-scrollbar min-h-0 flex-1 overflow-x-hidden overflow-y-auto overscroll-contain">
				<FileContentPane annotation={annotation} path={path} sessionId={sessionId} split={split} />
			</div>
		</section>
	);
}
