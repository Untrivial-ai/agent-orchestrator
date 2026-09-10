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
	type SerializedLexicalNode,
	type Spread,
} from "lexical";
import {
	forwardRef,
	useCallback,
	useEffect,
	useImperativeHandle,
	type ClipboardEvent,
	type JSX,
	type KeyboardEvent,
} from "react";
import { Box } from "lucide-react";
import { cn } from "../../lib/utils";
import { composerFileIcon } from "./composerFileIcon";
import { findActiveTrigger, type TriggerKind } from "./composerSuggest";

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
	getSnapshot(): ComposerEditorSnapshot;
};

type TokenKind = "skill" | "file";

const completionHandledEvents = new WeakSet<Event>();

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
	const wire = kind === "skill" ? `/${value}` : /\s/.test(value) ? `"${value}"` : value;
	const slash = value.lastIndexOf("/");
	const display = kind === "skill" ? wire : slash >= 0 ? value.slice(slash + 1) : value;
	return new ComposerTokenNode(kind, value, display, wire);
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

function $replaceEditorText(text: string): void {
	const root = $getRoot();
	root.clear();
	for (const line of text.split("\n")) {
		const paragraph = $createParagraphNode();
		if (line !== "") paragraph.append($createTextNode(line));
		root.append(paragraph);
	}
	root.selectEnd();
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
	editor.getRootElement()?.focus();
	editor.update(() => {
		const selection = $getSelection();
		if (!$isRangeSelection(selection)) $getRoot().selectEnd();
	});
}

const EditorBridge = forwardRef<
	ComposerEditorHandle,
	{
		disabled?: boolean;
		onChange: (snapshot: ComposerEditorSnapshot) => void;
		onComplete: (snapshot: ComposerEditorSnapshot, key: "Enter" | "Tab") => string | undefined;
		/** Return true to consume Enter before Lexical inserts a newline. */
		onEnterKey?: (event: globalThis.KeyboardEvent) => boolean;
	}
>(function EditorBridge({ disabled, onChange, onComplete, onEnterKey }, ref) {
	const [editor] = useLexicalComposerContext();

	useEffect(() => editor.setEditable(!disabled), [disabled, editor]);

	useImperativeHandle(
		ref,
		() => ({
			focus: () => focusEditor(editor),
			clear: () => {
				editor.update(() => $replaceEditorText(""), { discrete: true });
				editor.dispatchCommand(CLEAR_HISTORY_COMMAND, undefined);
			},
			setText: (text) => {
				editor.update(() => $replaceEditorText(text), { discrete: true });
				editor.dispatchCommand(CLEAR_HISTORY_COMMAND, undefined);
			},
			insertToken: (trigger, value) => {
				editor.update(() => {
					$insertComposerToken(trigger, value);
				});
			},
			getSnapshot: () => editor.getEditorState().read(editorSnapshot),
		}),
		[editor],
	);

	useEffect(
		() =>
			editor.registerUpdateListener(({ editorState }) => {
				editorState.read(() => onChange(editorSnapshot()));
			}),
		[editor, onChange],
	);

	useEffect(() => {
		const complete = (event: globalThis.KeyboardEvent | null, key: "Enter" | "Tab") => {
			if (event?.isComposing || event?.shiftKey || editor.isComposing()) return false;
			const snapshot = editorSnapshot();
			const value = onComplete(snapshot, key);
			if (!snapshot.trigger || !value) return false;
			if (!$insertComposerToken(snapshot.trigger, value)) return false;
			if (event) {
				completionHandledEvents.add(event);
				event.preventDefault();
			}
			return true;
		};
		const removeEnter = editor.registerCommand(
			KEY_ENTER_COMMAND,
			(event) => {
				if (event?.isComposing || event?.shiftKey || editor.isComposing()) return false;
				if (complete(event, "Enter")) return true;
				if (event && onEnterKey?.(event)) {
					event.preventDefault();
					return true;
				}
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
	}, [editor, onComplete, onEnterKey]);

	return null;
});

export const ComposerEditor = forwardRef<
	ComposerEditorHandle,
	{
		disabled?: boolean;
		label: string;
		placeholder: string;
		menuOpen: boolean;
		menuId: string;
		activeIndex: number;
		onChange: (snapshot: ComposerEditorSnapshot) => void;
		onComplete: (snapshot: ComposerEditorSnapshot, key: "Enter" | "Tab") => string | undefined;
		onEnterKey?: (event: globalThis.KeyboardEvent) => boolean;
		onCompositionChange: (isComposing: boolean) => void;
		onKeyDown: (event: KeyboardEvent<HTMLDivElement>) => void;
		onPaste: (event: ClipboardEvent<HTMLDivElement>) => void;
	}
>(function ComposerEditor(
	{
		disabled,
		label,
		placeholder,
		menuOpen,
		menuId,
		activeIndex,
		onChange,
		onComplete,
		onEnterKey,
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

	const placeholderNode = useCallback(
		() => (
			<div className="pointer-events-none absolute inset-x-0 top-0 py-1 pl-[7px] text-base! leading-relaxed text-muted-foreground">
				{placeholder}
			</div>
		),
		[placeholder],
	);

	return (
		<LexicalComposer initialConfig={initialConfig}>
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
								disabled && "opacity-50",
							)}
						/>
					}
					ErrorBoundary={LexicalErrorBoundary}
				/>
				<HistoryPlugin />
				<EditorBridge
					ref={ref}
					disabled={disabled}
					onChange={onChange}
					onComplete={onComplete}
					onEnterKey={onEnterKey}
				/>
			</div>
		</LexicalComposer>
	);
});
