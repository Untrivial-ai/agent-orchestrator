import { memo, useCallback, useEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { flushSync } from "react-dom";
import {
	DndContext,
	PointerSensor,
	closestCenter,
	type DragEndEvent,
	type DragStartEvent,
	type Modifier,
	useSensor,
	useSensors,
} from "@dnd-kit/core";
import { SortableContext, useSortable, verticalListSortingStrategy } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { ChevronDown, Circle, CornerDownLeft, GripVertical, Pencil, Trash2 } from "lucide-react";
import { cn } from "../../lib/utils";
import type { ConversationMessage } from "../../types/conversation";

export type QueuedMessage = { turnId: string; message: ConversationMessage };

const QUEUE_DOCK_VISIBLE_ROWS = 5;
const REORDER_ACTIVATION_DISTANCE = 4;

/** Keep queue drags inside the scroll list, matching sidebar session rows. */
const restrictToQueueDockBounds: Modifier = ({ activeNodeRect, containerNodeRect, transform }) => {
	if (!activeNodeRect || !containerNodeRect) return transform;
	const minY = containerNodeRect.top - activeNodeRect.top;
	const maxY = containerNodeRect.bottom - activeNodeRect.bottom;
	return {
		...transform,
		x: 0,
		y: Math.min(maxY, Math.max(minY, transform.y)),
	};
};

type DragRowSnapshot = {
	showPersistentSteer: boolean;
	showHoverSteer: boolean;
};

function reorderById(ids: string[], activeId: string, overId: string): string[] | null {
	if (activeId === overId) return null;
	const from = ids.indexOf(activeId);
	const to = ids.indexOf(overId);
	if (from < 0 || to < 0) return null;
	const next = [...ids];
	const [moved] = next.splice(from, 1);
	next.splice(to, 0, moved);
	return next;
}

function sortableRowStyle({
	transform,
	transition,
	isDragging,
	isReordering,
}: {
	transform: { x: number; y: number; scaleX: number; scaleY: number } | null;
	transition: string | undefined;
	isDragging: boolean;
	isReordering: boolean;
}): CSSProperties {
	return {
		transform: transform ? CSS.Transform.toString({ ...transform, x: 0, scaleX: 1, scaleY: 1 }) : undefined,
		transition: isDragging || isReordering ? "none" : transition,
	};
}

function QueuedMessageRowContent({
	message,
	showPersistentSteer,
	showHoverSteer = false,
	canSteer,
	suppressHoverSteer,
	onPromoteQueuedTurn,
	onBeginQueuedEdit,
	onCancelQueuedTurn,
	turnId,
	busy,
	onRunAction,
	reorderEnabled,
	dragHandleRef,
	dragHandleProps,
}: {
	message: ConversationMessage;
	showPersistentSteer?: boolean;
	showHoverSteer?: boolean;
	canSteer?: boolean;
	suppressHoverSteer?: boolean;
	onPromoteQueuedTurn?: (turnId: string) => Promise<unknown>;
	onBeginQueuedEdit?: (turnId: string, text: string) => void;
	onCancelQueuedTurn?: (turnId: string) => Promise<unknown>;
	turnId: string;
	busy?: boolean;
	onRunAction: (turnId: string, action: () => Promise<unknown>) => void;
	reorderEnabled?: boolean;
	dragHandleRef?: (element: HTMLButtonElement | null) => void;
	dragHandleProps?: Record<string, unknown>;
}) {
	const showHoverSteerButton =
		showHoverSteer ||
		Boolean(onPromoteQueuedTurn && canSteer && !showPersistentSteer && !suppressHoverSteer);

	return (
		<div className="queue-dock-row-content flex h-10 w-full min-w-0 items-center gap-2.5 overflow-hidden px-3">
			<Circle
				aria-hidden="true"
				className="size-3 shrink-0 text-muted-foreground/60"
				strokeWidth={1.5}
			/>
			<div className="min-w-0 flex-1 overflow-hidden">
				<p
					className="queue-dock-row-text truncate text-xs leading-relaxed text-foreground"
					title={message.text}
				>
					{message.text}
				</p>
			</div>
			<div className="queue-dock-actions flex shrink-0 items-center gap-0.5 whitespace-nowrap">
				{showHoverSteerButton ? (
					<button
						type="button"
						disabled={busy}
						onClick={() => {
							void onRunAction(turnId, () => onPromoteQueuedTurn!(turnId));
						}}
						className={cn(
							"inline-flex h-7 items-center rounded-lg px-2 text-[11px] leading-none text-muted-foreground transition-[background-color,color] duration-150 ease-out hover:bg-interactive-hover hover:text-foreground focus-visible:pointer-events-auto focus-visible:opacity-100 disabled:opacity-50 motion-reduce:transition-none",
							showHoverSteer
								? "pointer-events-none opacity-100"
								: "pointer-events-none opacity-0 group-hover/queued-row:pointer-events-auto group-hover/queued-row:opacity-100",
						)}
						aria-label="Steer this queued message into the running turn"
						title="Steer into running turn"
					>
						Steer
					</button>
				) : null}
				{showPersistentSteer && onPromoteQueuedTurn ? (
					<button
						type="button"
						disabled={busy}
						onClick={() => {
							void onRunAction(turnId, () => onPromoteQueuedTurn(turnId));
						}}
						className="inline-flex h-7 items-center gap-1.5 rounded-lg px-2 text-[11px] leading-none text-muted-foreground transition-[scale,background-color,color] duration-150 ease-out hover:bg-interactive-hover hover:text-foreground active:scale-[0.96] disabled:pointer-events-none disabled:opacity-50 motion-reduce:transition-none motion-reduce:active:scale-100"
						aria-label="Steer this queued message into the running turn"
						title="Steer into running turn"
					>
						<span className="inline-flex shrink-0 items-center gap-1 text-muted-foreground">
							<CornerDownLeft aria-hidden="true" className="shrink-0" width={12} height={12} strokeWidth={2} />
						</span>
						Steer
					</button>
				) : null}
				{onBeginQueuedEdit ? (
					<button
						type="button"
						disabled={busy}
						onClick={() => onBeginQueuedEdit(turnId, message.text)}
						className="flex size-7 items-center justify-center rounded-md text-muted-foreground transition-[scale,background-color,color] duration-150 ease-out hover:bg-interactive-hover hover:text-foreground active:scale-[0.96] disabled:pointer-events-none disabled:opacity-50 motion-reduce:transition-none motion-reduce:active:scale-100"
						aria-label="Edit queued message"
						title="Edit"
					>
						<Pencil aria-hidden="true" className="shrink-0" width={12} height={12} strokeWidth={2} />
					</button>
				) : null}
				{onCancelQueuedTurn ? (
					<button
						type="button"
						disabled={busy}
						onClick={() => {
							void onRunAction(turnId, () => onCancelQueuedTurn(turnId));
						}}
						className="flex size-7 items-center justify-center rounded-md text-muted-foreground transition-[scale,background-color,color] duration-150 ease-out hover:bg-interactive-hover hover:text-destructive active:scale-[0.96] disabled:pointer-events-none disabled:opacity-50 motion-reduce:transition-none motion-reduce:active:scale-100"
						aria-label="Delete queued message"
						title="Delete"
					>
						<Trash2 aria-hidden="true" className="shrink-0" width={12} height={12} strokeWidth={2} />
					</button>
				) : null}
				{reorderEnabled ? (
					<button
						type="button"
						ref={dragHandleRef}
						{...dragHandleProps}
						disabled={busy}
						className="flex size-7 cursor-grab items-center justify-center rounded-md text-muted-foreground touch-none active:cursor-grabbing disabled:pointer-events-none disabled:opacity-50"
						aria-label="Drag to reorder queued message"
						title="Drag to reorder"
					>
						<GripVertical aria-hidden="true" className="shrink-0" width={12} height={12} strokeWidth={2} />
					</button>
				) : null}
			</div>
		</div>
	);
}

function SortableQueuedMessageRow({
	turnId,
	message,
	hiddenFromView,
	showPersistentSteer,
	canSteer,
	suppressHoverSteer,
	onPromoteQueuedTurn,
	onBeginQueuedEdit,
	onCancelQueuedTurn,
	busy,
	error,
	onRunAction,
	reorderEnabled,
	isReordering,
	dragSnapshot,
	onRowHoverChange,
}: {
	turnId: string;
	message: ConversationMessage;
	hiddenFromView?: boolean;
	showPersistentSteer?: boolean;
	canSteer?: boolean;
	suppressHoverSteer?: boolean;
	onPromoteQueuedTurn?: (turnId: string) => Promise<unknown>;
	onBeginQueuedEdit?: (turnId: string, text: string) => void;
	onCancelQueuedTurn?: (turnId: string) => Promise<unknown>;
	busy?: boolean;
	error?: string;
	onRunAction: (turnId: string, action: () => Promise<unknown>) => void;
	reorderEnabled?: boolean;
	isReordering?: boolean;
	dragSnapshot?: DragRowSnapshot | null;
	onRowHoverChange?: (turnId: string | null) => void;
}) {
	const { attributes, listeners, setActivatorNodeRef, setNodeRef, transform, transition, isDragging } =
		useSortable({
			id: turnId,
			disabled: !reorderEnabled,
		});

	const frozenActions = isDragging && dragSnapshot ? dragSnapshot : null;

	return (
		<div
			ref={setNodeRef}
			className={cn(
				"queue-dock-row group/queued-row w-full min-w-0 max-w-full overflow-hidden",
				isDragging && "relative cursor-grabbing opacity-60",
			)}
			style={sortableRowStyle({
				transform,
				transition,
				isDragging,
				isReordering: Boolean(isReordering),
			})}
			data-dragging={isDragging ? "true" : undefined}
			data-testid={`queued-message-${turnId}`}
			aria-hidden={hiddenFromView ? true : undefined}
			inert={hiddenFromView ? true : undefined}
			onPointerEnter={() => {
				if (!isReordering) onRowHoverChange?.(turnId);
			}}
			onPointerLeave={() => {
				if (!isReordering) onRowHoverChange?.(null);
			}}
		>
			<QueuedMessageRowContent
				busy={busy}
				canSteer={canSteer}
				dragHandleProps={{ ...attributes, ...listeners }}
				dragHandleRef={setActivatorNodeRef}
				message={message}
				onBeginQueuedEdit={onBeginQueuedEdit}
				onCancelQueuedTurn={onCancelQueuedTurn}
				onPromoteQueuedTurn={onPromoteQueuedTurn}
				onRunAction={onRunAction}
				reorderEnabled={reorderEnabled}
				showHoverSteer={frozenActions?.showHoverSteer}
				showPersistentSteer={frozenActions?.showPersistentSteer ?? showPersistentSteer}
				suppressHoverSteer={suppressHoverSteer && !isDragging}
				turnId={turnId}
			/>
			{error ? (
				<p role="status" className="px-3 pb-2 text-[11px] text-warning">
					{error}
				</p>
			) : null}
		</div>
	);
}

export const QueuedMessageDock = memo(function QueuedMessageDock({
	messages,
	disabled,
	editingTurnId,
	canSteer,
	canSteerNext,
	steerNextRequest,
	onPromoteQueuedTurn,
	onBeginQueuedEdit,
	onCancelQueuedTurn,
	onReorderQueuedTurns,
	promotePendingTurnId,
	cancelPendingTurnId,
}: {
	messages: QueuedMessage[];
	disabled?: boolean;
	editingTurnId?: string;
	canSteer?: boolean;
	canSteerNext?: boolean;
	steerNextRequest?: number;
	onPromoteQueuedTurn?: (turnId: string) => Promise<unknown>;
	onBeginQueuedEdit?: (turnId: string, text: string) => void;
	onCancelQueuedTurn?: (turnId: string) => Promise<unknown>;
	onReorderQueuedTurns?: (turnIds: string[]) => Promise<unknown>;
	promotePendingTurnId?: string;
	cancelPendingTurnId?: string;
}) {
	const [expanded, setExpanded] = useState(true);
	const [errors, setErrors] = useState<Record<string, string>>({});
	const [optimisticallySteeredTurnIds, setOptimisticallySteeredTurnIds] = useState<Set<string>>(
		() => new Set(),
	);
	const [displayOrder, setDisplayOrder] = useState<string[] | null>(null);
	const [hoverSteerSuppressed, setHoverSteerSuppressed] = useState(false);
	const [reorderError, setReorderError] = useState<string | undefined>();
	const [isReordering, setIsReordering] = useState(false);
	const [dragSnapshot, setDragSnapshot] = useState<DragRowSnapshot | null>(null);
	const [hoveredTurnId, setHoveredTurnId] = useState<string | null>(null);
	const scrollRef = useRef<HTMLDivElement>(null);
	const hoverSteerSuppressTimer = useRef<number | undefined>(undefined);
	const [handledSteerNextRequest, setHandledSteerNextRequest] = useState(steerNextRequest ?? 0);
	const steerNextInFlight = useRef(false);
	const reorderSensors = useSensors(
		useSensor(PointerSensor, {
			activationConstraint: { distance: REORDER_ACTIVATION_DISTANCE },
		}),
	);

	const runAction = useCallback(
		async (turnId: string, action: () => Promise<unknown>) => {
			setErrors((current) => {
				if (!current[turnId]) return current;
				const next = { ...current };
				delete next[turnId];
				return next;
			});
			try {
				await action();
			} catch (error) {
				setErrors((current) => ({
					...current,
					[turnId]: error instanceof Error ? error.message : "That action failed.",
				}));
			}
		},
		[],
	);

	const visibleMessages = messages.filter(({ turnId }) => !optimisticallySteeredTurnIds.has(turnId));
	const count = visibleMessages.length;
	const hasMore = count > 1;
	const isOpen = !hasMore || expanded;
	const expandedRows = Math.min(count, QUEUE_DOCK_VISIBLE_ROWS);
	const messagesByTurnId = useMemo(
		() => new Map(visibleMessages.map((message) => [message.turnId, message])),
		[visibleMessages],
	);
	const fifoTurnIds = useMemo(() => visibleMessages.map(({ turnId }) => turnId), [visibleMessages]);
	const defaultDisplayTurnIds = useMemo(() => [...fifoTurnIds].reverse(), [fifoTurnIds]);
	const displayTurnIds = useMemo(() => {
		if (!displayOrder) return defaultDisplayTurnIds;
		const known = new Set(fifoTurnIds);
		const ordered = displayOrder.filter((turnId) => known.has(turnId));
		const missing = fifoTurnIds.filter((turnId) => !ordered.includes(turnId));
		return [...ordered, ...missing];
	}, [defaultDisplayTurnIds, displayOrder, fifoTurnIds]);
	const displayMessages = useMemo(
		() =>
			displayTurnIds.flatMap((turnId) => {
				const message = messagesByTurnId.get(turnId);
				return message ? [message] : [];
			}),
		[displayTurnIds, messagesByTurnId],
	);
	const reorderEnabled =
		Boolean(onReorderQueuedTurns) && count > 1 && isOpen;
	const nextQueuedTurnId = fifoTurnIds[0];

	const suppressHoverSteer = useCallback(() => {
		setHoverSteerSuppressed(true);
		if (hoverSteerSuppressTimer.current !== undefined) {
			window.clearTimeout(hoverSteerSuppressTimer.current);
		}
		hoverSteerSuppressTimer.current = window.setTimeout(() => {
			setHoverSteerSuppressed(false);
			hoverSteerSuppressTimer.current = undefined;
		}, 200);
	}, []);

	useEffect(
		() => () => {
			if (hoverSteerSuppressTimer.current !== undefined) {
				window.clearTimeout(hoverSteerSuppressTimer.current);
			}
		},
		[],
	);

	const fifoTurnIdSetKey = useMemo(
		() => [...fifoTurnIds].sort().join("\0"),
		[fifoTurnIds],
	);

	useEffect(() => {
		setDisplayOrder(null);
	}, [fifoTurnIdSetKey]);

	useEffect(() => {
		if (!displayOrder) return;
		const expectedFifo = [...displayOrder].reverse();
		if (
			expectedFifo.length === fifoTurnIds.length &&
			expectedFifo.every((turnId, index) => turnId === fifoTurnIds[index])
		) {
			setDisplayOrder(null);
		}
	}, [displayOrder, fifoTurnIds]);

	const promoteQueuedTurn = useCallback(
		async (turnId: string) => {
			setOptimisticallySteeredTurnIds((current) => new Set(current).add(turnId));
			try {
				if (!onPromoteQueuedTurn) return;
				await onPromoteQueuedTurn(turnId);
			} catch (error) {
				setOptimisticallySteeredTurnIds((current) => {
					const next = new Set(current);
					next.delete(turnId);
					return next;
				});
				throw error;
			}
		},
		[onPromoteQueuedTurn],
	);

	useEffect(() => {
		if (steerNextRequest === undefined || steerNextRequest <= handledSteerNextRequest) return;
		if (!canSteerNext || !onPromoteQueuedTurn) {
			setHandledSteerNextRequest(steerNextRequest);
			return;
		}
		if (steerNextInFlight.current) return;

		const next = displayMessages.find((message) => message.turnId === nextQueuedTurnId);
		if (!next) {
			setHandledSteerNextRequest(steerNextRequest);
			return;
		}
		steerNextInFlight.current = true;
		void runAction(next.turnId, () => promoteQueuedTurn(next.turnId)).finally(() => {
			steerNextInFlight.current = false;
			setHandledSteerNextRequest((request) => request + 1);
		});
	}, [
		canSteerNext,
		displayMessages,
		handledSteerNextRequest,
		nextQueuedTurnId,
		onPromoteQueuedTurn,
		promoteQueuedTurn,
		runAction,
		steerNextRequest,
	]);

	useEffect(() => {
		if (!isOpen || count < QUEUE_DOCK_VISIBLE_ROWS) return;
		const scroll = scrollRef.current;
		if (scroll) scroll.scrollTop = scroll.scrollHeight;
	}, [count, isOpen]);

	const onDragCancel = useCallback(() => {
		setIsReordering(false);
		setDragSnapshot(null);
		setHoveredTurnId(null);
		suppressHoverSteer();
	}, [suppressHoverSteer]);

	const onDragStart = useCallback(
		({ active }: DragStartEvent) => {
			const turnId = String(active.id);
			const showPersistentSteer = Boolean(
				canSteer && canSteerNext && turnId === nextQueuedTurnId,
			);
			setIsReordering(true);
			setDragSnapshot({
				showPersistentSteer,
				showHoverSteer: Boolean(
					canSteer && !showPersistentSteer && hoveredTurnId === turnId,
				),
			});
			suppressHoverSteer();
		},
		[canSteer, canSteerNext, hoveredTurnId, nextQueuedTurnId, suppressHoverSteer],
	);

	const onDragEnd = useCallback(
		({ active, over }: DragEndEvent) => {
			setIsReordering(false);
			setDragSnapshot(null);
			setHoveredTurnId(null);
			suppressHoverSteer();
			if (!over || !onReorderQueuedTurns) return;
			const nextDisplay = reorderById(displayTurnIds, String(active.id), String(over.id));
			if (!nextDisplay) return;
			const fifoOrder = [...nextDisplay].reverse();
			setReorderError(undefined);
			flushSync(() => setDisplayOrder(nextDisplay));
			void onReorderQueuedTurns(fifoOrder).catch((error) => {
				setDisplayOrder(null);
				setReorderError(
					error instanceof Error ? error.message : "Could not reorder queued messages.",
				);
			});
		},
		[displayTurnIds, onReorderQueuedTurns, suppressHoverSteer],
	);

	const rowProps = {
		canSteer,
		onPromoteQueuedTurn: onPromoteQueuedTurn ? promoteQueuedTurn : undefined,
		onBeginQueuedEdit,
		onCancelQueuedTurn,
		onRunAction: runAction,
		suppressHoverSteer: hoverSteerSuppressed || isReordering,
	};

	return (
		<div
			className="queue-dock overflow-hidden rounded-[var(--radius-chat-composer)] border border-border-strong bg-surface shadow-sm"
			data-testid="queued-message-dock"
			data-collapsible={hasMore ? "true" : "false"}
			data-expanded={isOpen ? "true" : "false"}
			data-reordering={isReordering ? "true" : undefined}
			style={{ "--queue-dock-expanded-rows": expandedRows } as CSSProperties}
		>
			<button
				type="button"
				onClick={() => hasMore && setExpanded((open) => !open)}
				disabled={!hasMore}
				className={cn(
					"queue-dock-toggle relative z-[1] flex w-full items-center gap-2 bg-surface px-3 py-2 text-left motion-reduce:transition-none",
					hasMore && "cursor-pointer",
					!hasMore && "cursor-default",
				)}
				aria-expanded={hasMore ? expanded : undefined}
				data-testid="queued-message-toggle"
			>
				<span
					aria-hidden="true"
					className={cn(
						"grid h-3.5 shrink-0 overflow-hidden transition-[width] duration-300 ease-[cubic-bezier(0.16,1,0.3,1)] motion-reduce:transition-none",
						hasMore ? "w-3.5" : "w-0",
					)}
				>
					<ChevronDown
						className={cn(
							"size-3.5 shrink-0 text-muted-foreground transition-transform duration-300 ease-[cubic-bezier(0.16,1,0.3,1)] motion-reduce:transition-none",
							expanded ? "rotate-0" : "-rotate-90",
						)}
					/>
				</span>
				<span className="queue-dock-label text-xs font-medium text-muted-foreground transition-colors duration-150">
					<span
						className="inline-block w-fit"
					>
						{count} Queued {count === 1 ? "Message" : "Messages"}
					</span>
					{editingTurnId ? " · editing" : ""}
				</span>
			</button>
			{count > 0 ? (
				<div
					className="queue-dock-body bg-surface"
					data-expanded={isOpen ? "true" : "false"}
					data-collapsible={hasMore ? "true" : "false"}
					style={
						{
							"--queue-dock-expanded-rows": expandedRows,
						} as CSSProperties
					}
				>
					<DndContext
						collisionDetection={closestCenter}
						id="queue-dock-reorder"
						modifiers={[restrictToQueueDockBounds]}
						onDragCancel={onDragCancel}
						onDragEnd={onDragEnd}
						onDragStart={onDragStart}
						sensors={reorderSensors}
					>
						<SortableContext items={displayTurnIds} strategy={verticalListSortingStrategy}>
							<div
								ref={scrollRef}
								className={cn(
									"queue-dock-scroll",
									isOpen && count >= QUEUE_DOCK_VISIBLE_ROWS && "queue-dock-scroll-active",
								)}
							>
								{displayMessages.map(({ turnId, message }, index) => {
									const busy =
										disabled || promotePendingTurnId === turnId || cancelPendingTurnId === turnId;
									const isNextQueuedTurn = turnId === nextQueuedTurnId;
									return (
										<SortableQueuedMessageRow
											key={turnId}
											turnId={turnId}
											message={message}
											hiddenFromView={!isOpen && index !== displayMessages.length - 1}
											showPersistentSteer={Boolean(canSteer && canSteerNext && isNextQueuedTurn)}
											busy={busy}
											dragSnapshot={dragSnapshot}
											error={errors[turnId]}
											isReordering={isReordering}
											onRowHoverChange={setHoveredTurnId}
											reorderEnabled={reorderEnabled && !busy}
											{...rowProps}
										/>
									);
								})}
							</div>
						</SortableContext>
					</DndContext>
					{reorderError ? (
						<p role="status" className="px-3 pb-2 text-[11px] text-warning">
							{reorderError}
						</p>
					) : null}
				</div>
			) : null}
		</div>
	);
});
