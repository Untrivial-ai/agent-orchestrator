import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { sessionWorkspaceFileQueryOptions } from "../hooks/useSessionWorkspaceFiles";
import {
	canSplitCompare,
	PanelMessage,
	ReviewDiffBody,
	RetryButton,
	type FileAnnotationModel,
} from "./WorkspaceDiffView";
import { ReadOnlyFileView } from "./ReadOnlyFileView";

export function FileContentPane({
	annotation,
	path,
	sessionId,
	split,
	wrap,
}: {
	annotation: FileAnnotationModel;
	path: string | null;
	sessionId: string;
	split: boolean;
	wrap: boolean;
}) {
	const { t } = useTranslation();
	// Mirrors WorkspaceDiffView's own guard: a background refetch mid-selection
	// would re-render the pane out from under an active text selection or its
	// context menu.
	const [selectionOrMenuActive, setSelectionOrMenuActive] = useState(false);
	const query = useQuery({
		...sessionWorkspaceFileQueryOptions(sessionId, path ?? "", t("files.error.loadWorkspaceFile")),
		enabled: Boolean(path) && !selectionOrMenuActive,
	});

	if (!path) {
		return <PanelMessage>{t("files.explorer.selectFile")}</PanelMessage>;
	}
	if (query.isPending) {
		return <PanelMessage>{t("files.loadingDiff")}</PanelMessage>;
	}
	if (query.error) {
		return (
			<PanelMessage action={<RetryButton onClick={() => void query.refetch()} />}>
				{query.error.message || t("files.error.loadFile")}
			</PanelMessage>
		);
	}
	if (!query.data) return null;

	const detail = query.data;
	if (detail.status !== "unmodified") {
		return (
			<ReviewDiffBody
				annotation={annotation}
				detail={detail}
				detailLoadedAt={query.dataUpdatedAt}
				filePath={path}
				onActiveSelectionChange={setSelectionOrMenuActive}
				sessionId={sessionId}
				split={split && canSplitCompare(detail.status)}
				wrap={wrap}
			/>
		);
	}
	return <ReadOnlyFileView detail={detail} sessionId={sessionId} />;
}
