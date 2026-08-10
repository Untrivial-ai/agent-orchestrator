import {
	createContext,
	useContext,
	useEffect,
	useMemo,
	useState,
	type Dispatch,
	type ReactNode,
	type SetStateAction,
} from "react";
import type { FileAnnotationTarget } from "../../shared/file-annotations";

export type PendingReviewAnnotation = {
	key: string;
	target: FileAnnotationTarget & { rowIndex?: number };
	feedback: string;
};

type PendingReviewCommentsContextValue = {
	pendingAnnotations: Record<string, PendingReviewAnnotation>;
	setPendingAnnotations: Dispatch<SetStateAction<Record<string, PendingReviewAnnotation>>>;
};

const PendingReviewCommentsContext = createContext<PendingReviewCommentsContextValue | null>(null);

/**
 * Holds staged inline review comments above both Files panel mounts (inspector +
 * maximized). Survives inspector tab switches that unmount SessionFilesView and
 * keeps the two instances on one shared collection.
 */
export function PendingReviewCommentsProvider({
	sessionId,
	children,
}: {
	sessionId: string;
	children: ReactNode;
}) {
	const [pendingAnnotations, setPendingAnnotations] = useState<Record<string, PendingReviewAnnotation>>({});

	useEffect(() => {
		setPendingAnnotations({});
	}, [sessionId]);

	const value = useMemo(
		() => ({ pendingAnnotations, setPendingAnnotations }),
		[pendingAnnotations],
	);

	return (
		<PendingReviewCommentsContext.Provider value={value}>{children}</PendingReviewCommentsContext.Provider>
	);
}

export function usePendingReviewComments(): PendingReviewCommentsContextValue {
	const context = useContext(PendingReviewCommentsContext);
	if (!context) {
		throw new Error("usePendingReviewComments must be used within PendingReviewCommentsProvider");
	}
	return context;
}
