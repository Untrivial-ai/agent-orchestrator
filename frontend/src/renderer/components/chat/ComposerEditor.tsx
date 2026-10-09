import { LexicalComposer } from "@lexical/react/LexicalComposer";
import { ContentEditable } from "@lexical/react/LexicalContentEditable";
import { LexicalErrorBoundary } from "@lexical/react/LexicalErrorBoundary";
import { HistoryPlugin } from "@lexical/react/LexicalHistoryPlugin";
import { PlainTextPlugin } from "@lexical/react/LexicalPlainTextPlugin";
import { useLexicalComposerContext } from "@lexical/react/LexicalComposerContext";
import {
	$getNodeByKey,
	$getRoot,
	$getSelection,
	$isElementNode,
	$isRangeSelection,
	$isTextNode,
	$createParagraphNode,
	$createTextNode,
	CLEAR_HISTORY_COMMAND,
	COMMAND_PRIORITY_HIGH,
	DecoratorNode,
	KEY_ENTER_COMMAND,
	KEY_TAB_COMMAND,
	type EditorConfig,
	type LexicalEditor,
	type LexicalNode,
	type NodeKey,
	type RangeSelection,
	type SerializedLexicalNode,
	type Spread,
} from "lexical";
import {
	createContext,
	forwardRef,
	useCallback,
	useContext,
	useEffect,
	useImperativeHandle,
	useRef,
	useState,
	type ClipboardEvent,
	type JSX,
	type KeyboardEvent,
} from "react";
import { Box, Image as ImageIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import { cn } from "../../lib/utils";
import { composerFileIcon } from "./composerFileIcon";
import { findActiveTrigger, type TriggerKind } from "./composerSuggest";
import { splitInlineImagePaths } from "./messageAttachments";

export type ComposerTrigger = {
	kind: TriggerKind;
	key: string;
	nodeKey: string;
	start: number;
	end: number;
	query: string;
};

export type ComposerEditorSnapshot = {
	text: string;
	hasText: boolean;
	trigger?: ComposerTrigger;
};

export type ComposerEditorHandle = {
	focus(): void;
	clear(): void;
	setText(text: string): void;
	insertToken(trigger: ComposerTrigger, value: string): void;
	/** Inserts a file reference chip at the caret (or the end) that sends `wire`. */
	insertReference(path: string, display: string, wire: string): void;
	/**
	 * Hold the caret's place for images still being staged. Returns a reservation
	 * for fillImages, so the chips land where the user pasted, not wherever the
	 * caret has moved by the time staging finishes.
	 */
	reserveImages(): string | undefined;
	/** Replace a reservation with one chip per staged image path (none removes it). */
	fillImages(reservation: string, paths: string[]): void;
	/** Drop every inline chip for an image that left the attachment list. */
	removeImage(path: string): void;
	/**
	 * Drop chips whose image is not in `attached` (an undo can restore a chip
	 * after its image was removed). Typed text is never touched.
	 */
	pruneImages(attached: string[]): void;
	getSnapshot(): ComposerEditorSnapshot;
};

/** What an inline image chip shows for a staged path; the path itself is the wire text. */
export type ComposerImage = { path: string; name: string; src?: string };

const ComposerImages = createContext<ComposerImage[]>([]);

/** An empty path is a reservation whose image is still being staged. */
function ComposerImageChip({ path }: { path: string }) {
	const { t } = useTranslation();
	const images = useContext(ComposerImages);
	const index = images.findIndex((candidate) => candidate.path === path);
	const image = images[index];
	if (!path) {
		return (
			<span
				data-composer-token="image-pending"
				contentEditable={false}
				className="mx-0.5 inline-flex items-center gap-1 rounded-md border border-dashed border-border-strong px-1.5 py-0.5 align-middle text-[0.9em] leading-none text-muted-foreground select-none"
			>
				<ImageIcon aria-hidden="true" className="size-3 shrink-0" />
				…
			</span>
		);
	}
	return (
		<span
			data-composer-token="image"
			data-value={path}
			contentEditable={false}
			title={image?.name}
			className="mx-0.5 inline-flex max-w-48 items-center gap-1 rounded-md border border-border-strong bg-interactive-hover py-0.5 pl-0.5 pr-1.5 align-middle text-[0.9em] leading-none text-foreground select-none"
		>
			{image?.src ? (
				<img src={image.src} alt="" className="size-4 shrink-0 rounded-sm object-cover" />
			) : (
				<ImageIcon aria-hidden="true" className="size-3 shrink-0" />
			)}
			{/* Numbered like the sent message will be; the file name stays in the tooltip. */}
			<span className="truncate">
				{image ? t("chat.image.numbered", { index: index + 1 }) : t("chat.image.untitled")}
			</span>
		</span>
	);
}

type TokenKind = "skill" | "file" | "image";

const completionHandledEvents = new WeakSet<Event>();
const PROGRAMMATIC_TEXT_UPDATE_TAG = "ao:composer-programmatic-text";

type SerializedComposerTokenNode = Spread<
	{
		kind: TokenKind;
		value: string;
		display: string;
		wire: string;
	},
	SerializedLexicalNode
>;

class ComposerTokenNode extends DecoratorNode<JSX.Element> {
	__kind: TokenKind;
	__value: string;
	__display: string;
	__wire: string;

	static getType(): string {
		return "composer-token";
	}

	static clone(node: ComposerTokenNode): ComposerTokenNode {
		return new ComposerTokenNode(
			node.__kind,
			node.__value,
			node.__display,
			node.__wire,
			node.__key,
		);
	}

	static importJSON(serialized: SerializedComposerTokenNode): ComposerTokenNode {
		return new ComposerTokenNode(
			serialized.kind,
			serialized.value,
			serialized.display,
			serialized.wire,
		);
	}

	constructor(kind: TokenKind, value: string, display: string, wire: string, key?: NodeKey) {
		super(key);
		this.__kind = kind;
		this.__value = value;
		this.__display = display;
		this.__wire = wire;
	}

	exportJSON(): SerializedComposerTokenNode {
		return {
			...super.exportJSON(),
			type: "composer-token",
			version: 1,
			kind: this.__kind,
			value: this.__value,
			display: this.__display,
			wire: this.__wire,
		};
	}

	createDOM(_config: EditorConfig): HTMLElement {
		return document.createElement("span");
	}

	updateDOM(): false {
		return false;
	}

	isInline(): true {
		return true;
	}

	isKeyboardSelectable(): false {
		return false;
	}

	getTextContent(): string {
		return this.__wire;
	}

	decorate(): JSX.Element {
		if (this.__kind === "image") return <ComposerImageChip path={this.__value} />;
		const Icon = this.__kind === "skill" ? Box : composerFileIcon(this.__value);
		return (
			<span
				data-composer-token={this.__kind}
				data-value={this.__value}
				contentEditable={false}
				className={cn(
					"mx-0.5 inline-flex items-center gap-1 rounded-md border border-border-strong bg-interactive-hover px-1.5 py-0.5 align-middle text-[0.9em] leading-none select-none",
					this.__kind === "skill"
						? "text-logo-accent"
						: "text-foreground",
				)}
			>
				<Icon aria-hidden="true" className="size-3 shrink-0" />
				{this.__display}
			</span>
		);
	}
}

function $createComposerTokenNode(kind: TokenKind, value: string): ComposerTokenNode {
	if (kind === "image") return new ComposerTokenNode(kind, value, value, value);
	const wire = kind === "skill" ? `/${value}` : /\s/.test(value) ? `"${value}"` : value;
	const slash = value.lastIndexOf("/");
	const display = kind === "skill" ? wire : slash >= 0 ? value.slice(slash + 1) : value;
	return new ComposerTokenNode(kind, value, display, wire);
}

function $insertComposerReference(path: string, display: string, wire: string): void {
	let selection = $getSelection();
	if (!$isRangeSelection(selection)) {
		$getRoot().selectEnd();
		selection = $getSelection();
	}
	if (!$isRangeSelection(selection)) return;
	selection.insertNodes([new ComposerTokenNode("file", path, display, wire), $createTextNode(" ")]);
}

function $serializeComposer(): string {
	return $getRoot()
		.getChildren()
		.map((child) => child.getTextContent())
		.join("\n");
}

function $insertComposerToken(trigger: ComposerTrigger, value: string): boolean {
	const node = $getNodeByKey<LexicalNode>(trigger.nodeKey);
	if (!$isTextNode(node)) return false;
	const text = node.getTextContent();
	const expected = `${trigger.kind === "skill" ? "/" : "@"}${trigger.query}`;
	if (text.slice(trigger.start, trigger.end) !== expected) return false;

	const before = text.slice(0, trigger.start);
	const after = text.slice(trigger.end);
	const token = $createComposerTokenNode(trigger.kind, value);
	const tail = $createTextNode(/^\s/.test(after) ? after : ` ${after}`);
	if (before) {
		const head = $createTextNode(before);
		node.replace(head);
		head.insertAfter(token);
	} else {
		node.replace(token);
	}
	token.insertAfter(tail);
	tail.select(1, 1);
	return true;
}

function $replaceEditorText(text: string, attached: string[] = []): void {
	const root = $getRoot();
	root.clear();
	for (const line of text.split("\n")) {
		const paragraph = $createParagraphNode();
		// A restored draft is plain text; paths of still-attached images become chips
		// again. Any other path stays the text the user typed.
		for (const segment of splitInlineImagePaths(line, (path) => attached.includes(path))) {
			paragraph.append(
				segment.path === undefined
					? $createTextNode(segment.text)
					: $createComposerTokenNode("image", segment.path),
			);
		}
		root.append(paragraph);
	}
	root.selectEnd();
}

/** True when the character before the caret would run into an inserted chip's text. */
function $joinsPreviousWord(selection: RangeSelection): boolean {
	const { anchor } = selection;
	const node = anchor.getNode();
	const before = $isTextNode(node)
		? node.getTextContent().slice(0, anchor.offset) || (node.getPreviousSibling()?.getTextContent() ?? "")
		: $isElementNode(node) && anchor.offset > 0
			? (node.getChildAtIndex(anchor.offset - 1)?.getTextContent() ?? "")
			: "";
	return /\S$/.test(before);
}

function $reserveImages(): string | undefined {
	const reservation = $createComposerTokenNode("image", "");
	const nodes: LexicalNode[] = [reservation, $createTextNode(" ")];
	let selection = $getSelection();
	if (!$isRangeSelection(selection)) {
		// A drop or the file picker can leave no selection; the end is the natural place.
		$getRoot().selectEnd();
		selection = $getSelection();
	}
	if (!$isRangeSelection(selection)) return undefined;
	if ($joinsPreviousWord(selection)) nodes.unshift($createTextNode(" "));
	selection.insertNodes(nodes);
	return reservation.getKey();
}

function $fillImages(reservation: string, paths: string[]): void {
	const node = $getNodeByKey(reservation);
	// Deleted while staging: the user removed it on purpose, so add nothing back.
	if (!(node instanceof ComposerTokenNode)) return;
	if (paths.length === 0) {
		$removeChip(node);
		return;
	}
	let last: LexicalNode = node;
	paths.forEach((path, index) => {
		const chip = $createComposerTokenNode("image", path);
		if (index === 0) {
			node.replace(chip);
		} else {
			const space = $createTextNode(" ");
			last.insertAfter(space);
			space.insertAfter(chip);
		}
		last = chip;
	});
}

/** Remove a chip and one following space so the words around it don't double-space. */
function $removeChip(chip: LexicalNode): void {
	const next = chip.getNextSibling();
	if ($isTextNode(next) && next.getTextContent().startsWith(" ")) {
		if (next.getTextContent() === " ") next.remove();
		else next.setTextContent(next.getTextContent().slice(1));
	}
	chip.remove();
}

function $removeImageTokens(remove: (path: string) => boolean): void {
	for (const paragraph of $getRoot().getChildren()) {
		if (!$isElementNode(paragraph)) continue;
		for (const child of paragraph.getChildren()) {
			if (child instanceof ComposerTokenNode && child.__kind === "image" && remove(child.__value)) $removeChip(child);
		}
	}
}

function editorSnapshot(): ComposerEditorSnapshot {
	const text = $serializeComposer();
	const selection = $getSelection();
	if (!$isRangeSelection(selection) || !selection.isCollapsed()) {
		return { text, hasText: text.trim().length > 0 };
	}

	const anchor = selection.anchor;
	const node = anchor.getNode();
	if (!$isTextNode(node) || anchor.type !== "text") {
		return { text, hasText: text.trim().length > 0 };
	}

	const active = findActiveTrigger(node.getTextContent(), anchor.offset);
	if (!active) return { text, hasText: text.trim().length > 0 };
	return {
		text,
		hasText: text.trim().length > 0,
		trigger: {
			...active,
			key: `${node.getKey()}:${active.start}`,
			nodeKey: node.getKey(),
			end: anchor.offset,
		},
	};
}

function focusEditor(editor: LexicalEditor): void {
	editor.focus();
	const root = editor.getRootElement();
	if (root && document.activeElement !== root) root.focus({ preventScroll: true });
}

const EditorBridge = forwardRef<
	ComposerEditorHandle,
	{
		disabled?: boolean;
		enterSends: boolean;
		/** Staged paths of the images attached right now. */
		attachedImages: () => string[];
		onChange: (snapshot: ComposerEditorSnapshot) => void;
		onComplete: (snapshot: ComposerEditorSnapshot, key: "Enter" | "Tab") => string | undefined;
		onEnter: (snapshot: ComposerEditorSnapshot, event: globalThis.KeyboardEvent) => boolean;
	}
>(function EditorBridge({ disabled, enterSends, attachedImages, onChange, onComplete, onEnter }, ref) {
	const [editor] = useLexicalComposerContext();

	useEffect(() => editor.setEditable(!disabled), [disabled, editor]);

	useImperativeHandle(
		ref,
		() => ({
			focus: () => focusEditor(editor),
			clear: () => {
				editor.update(() => {
					$replaceEditorText("");
					editor.dispatchCommand(CLEAR_HISTORY_COMMAND, undefined);
				}, {
					discrete: true,
					tag: PROGRAMMATIC_TEXT_UPDATE_TAG,
				});
			},
			setText: (text) => {
				editor.update(() => {
					$replaceEditorText(text, attachedImages());
					editor.dispatchCommand(CLEAR_HISTORY_COMMAND, undefined);
				}, {
					discrete: true,
					tag: PROGRAMMATIC_TEXT_UPDATE_TAG,
				});
			},
			insertToken: (trigger, value) => {
				editor.update(() => {
					$insertComposerToken(trigger, value);
				}, { discrete: true });
			},
			insertReference: (path, display, wire) => {
				editor.update(() => {
					$insertComposerReference(path, display, wire);
				}, { discrete: true });
			},
			reserveImages: () => {
				let reservation: string | undefined;
				editor.update(() => {
					reservation = $reserveImages();
				}, { discrete: true });
				return reservation;
			},
			fillImages: (reservation, paths) => {
				editor.update(() => $fillImages(reservation, paths), { discrete: true });
			},
			removeImage: (path) => {
				editor.update(() => $removeImageTokens((candidate) => candidate === path), { discrete: true });
			},
			pruneImages: (attached) => {
				editor.update(() => $removeImageTokens((candidate) => !attached.includes(candidate)), { discrete: true });
			},
			getSnapshot: () => editor.getEditorState().read(editorSnapshot),
		}),
		[attachedImages, editor],
	);

	useEffect(
		() =>
			editor.registerUpdateListener(({ editorState, tags }) => {
				if (tags.has(PROGRAMMATIC_TEXT_UPDATE_TAG)) return;
				editorState.read(() => onChange(editorSnapshot()));
			}),
		[editor, onChange],
	);

	useEffect(() => {
		const complete = (event: globalThis.KeyboardEvent | null, key: "Enter" | "Tab") => {
			// The React capture handler owns send/menu keys before Lexical's native
			// bubble listener. A prevented event was already handled there and must not
			// also insert a newline or a second completion token.
			if (event?.defaultPrevented) return true;
			if (event?.isComposing || event?.keyCode === 229 || event?.shiftKey || editor.isComposing()) return false;
			if (key === "Enter" && !enterSends && !event?.metaKey && !event?.ctrlKey) return false;
			const snapshot = editorSnapshot();
			const value = onComplete(snapshot, key);
			if (snapshot.trigger && value) {
				if (!$insertComposerToken(snapshot.trigger, value)) return false;
				if (event) {
					completionHandledEvents.add(event);
					event.preventDefault();
				}
				return true;
			}
			if (key === "Enter" && event && onEnter(snapshot, event)) {
				completionHandledEvents.add(event);
				event.preventDefault();
				return true;
			}
			return false;
		};
		const removeEnter = editor.registerCommand(
			KEY_ENTER_COMMAND,
			(event) => {
				// Claim only the editor command: the browser must still confirm the IME
				// candidate, without Lexical inserting a newline or selecting a suggestion.
				if (event?.isComposing || event?.keyCode === 229) return true;
				if (event?.shiftKey || editor.isComposing()) return false;
				if (complete(event, "Enter")) return true;
				return false;
			},
			COMMAND_PRIORITY_HIGH,
		);
		const removeTab = editor.registerCommand(
			KEY_TAB_COMMAND,
			(event) => complete(event, "Tab"),
			COMMAND_PRIORITY_HIGH,
		);
		return () => {
			removeEnter();
			removeTab();
		};
	}, [editor, enterSends, onComplete, onEnter]);

	return null;
});

/**
 * Fades a changing placeholder (the orchestrator's start-up steps) out, swaps
 * the text, and fades it back in. Fast on purpose. The first text mounts
 * directly.
 */
const PLACEHOLDER_FADE_MS = 80;

function FadingPlaceholder({ text }: { text: string }) {
	const [shown, setShown] = useState(text);
	useEffect(() => {
		if (text === shown) return;
		const timer = setTimeout(() => setShown(text), PLACEHOLDER_FADE_MS);
		return () => clearTimeout(timer);
	}, [text, shown]);
	return (
		<span
			className="transition-opacity ease-out motion-reduce:transition-none"
			style={{ opacity: text === shown ? 1 : 0, transitionDuration: `${PLACEHOLDER_FADE_MS}ms` }}
		>
			{shown}
		</span>
	);
}

export const ComposerEditor = forwardRef<
	ComposerEditorHandle,
	{
		disabled?: boolean;
		enterSends: boolean;
		/** Hides the text while a send is in flight; the draft is still held for recovery. */
		concealed?: boolean;
		label: string;
		placeholder: string;
		menuOpen: boolean;
		menuId: string;
		activeIndex: number;
		images?: ComposerImage[];
		onChange: (snapshot: ComposerEditorSnapshot) => void;
		onComplete: (snapshot: ComposerEditorSnapshot, key: "Enter" | "Tab") => string | undefined;
		onEnter: (snapshot: ComposerEditorSnapshot, event: globalThis.KeyboardEvent) => boolean;
		onCompositionChange: (isComposing: boolean) => void;
		onKeyDown: (event: KeyboardEvent<HTMLDivElement>) => void;
		onPaste: (event: ClipboardEvent<HTMLDivElement>) => void;
	}
>(function ComposerEditor(
	{
		disabled,
		enterSends,
		concealed,
		label,
		placeholder,
		menuOpen,
		menuId,
		activeIndex,
		images = [],
		onChange,
		onComplete,
		onEnter,
		onCompositionChange,
		onKeyDown,
		onPaste,
	},
	ref,
) {
	const initialConfig = {
		namespace: "AOChatComposer",
		nodes: [ComposerTokenNode],
		editable: !disabled,
		theme: { paragraph: "m-0" },
		onError(error: Error) {
			throw error;
		},
	};

	// Read at setText time, not render time, so a draft restore sees the
	// attachments it was restored with.
	const imagesRef = useRef(images);
	imagesRef.current = images;
	const attachedImages = useCallback(() => imagesRef.current.map((image) => image.path), []);

	const placeholderNode = useCallback(
		() => (
			<div className="pointer-events-none absolute inset-x-0 top-0 py-1 pl-[7px] text-base! leading-relaxed text-muted-foreground">
				<FadingPlaceholder text={placeholder} />
			</div>
		),
		[placeholder],
	);

	return (
		<LexicalComposer initialConfig={initialConfig}>
			<ComposerImages.Provider value={images}>
			<div className="relative">
				<PlainTextPlugin
					contentEditable={
						<ContentEditable
							aria-label={label}
							aria-placeholder={placeholder}
							placeholder={placeholderNode}
							aria-disabled={disabled || undefined}
							role="combobox"
							aria-expanded={menuOpen}
							aria-controls={menuOpen ? menuId : undefined}
							aria-activedescendant={
								menuOpen ? `${menuId}-option-${activeIndex}` : undefined
							}
							aria-autocomplete="list"
							onCompositionStart={() => onCompositionChange(true)}
							onCompositionEnd={() => onCompositionChange(false)}
							onKeyDown={(event) => {
								if (!completionHandledEvents.has(event.nativeEvent)) onKeyDown(event);
							}}
							onPasteCapture={(event) => {
								onPaste(event);
								if (event.defaultPrevented) event.stopPropagation();
							}}
							className={cn(
								"chat-composer-scrollbar max-h-40 min-h-[4.5rem] w-full overflow-y-auto overscroll-contain bg-transparent py-1 pl-[7px] pr-0 text-base! leading-relaxed text-foreground caret-foreground outline-none selection:bg-foreground selection:text-background",
								concealed ? "invisible" : disabled && "opacity-50",
							)}
						/>
					}
					ErrorBoundary={LexicalErrorBoundary}
				/>
				{concealed ? <div aria-hidden="true">{placeholderNode()}</div> : null}
				<HistoryPlugin />
				<EditorBridge
					ref={ref}
					disabled={disabled}
					enterSends={enterSends}
					attachedImages={attachedImages}
					onChange={onChange}
					onComplete={onComplete}
					onEnter={onEnter}
				/>
			</div>
			</ComposerImages.Provider>
		</LexicalComposer>
	);
});
