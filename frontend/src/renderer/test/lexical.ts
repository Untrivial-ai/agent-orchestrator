import { act } from "@testing-library/react";
import {
	$getRoot,
	$isTextNode,
	CONTROLLED_TEXT_INSERTION_COMMAND,
	type LexicalEditor,
} from "lexical";

function lexicalEditor(field: HTMLElement): LexicalEditor {
	const editor = (field as HTMLElement & { __lexicalEditor?: LexicalEditor }).__lexicalEditor;
	if (!editor) throw new Error("Expected a Lexical editor");
	return editor;
}

/** JSDOM does not perform native contenteditable insertion, so drive Lexical directly. */
export async function typeInLexicalEditor(field: HTMLElement, text: string): Promise<void> {
	const editor = lexicalEditor(field);
	await act(async () => {
		field.focus();
		editor.update(() => $getRoot().selectEnd(), { discrete: true });
		for (const character of text) {
			editor.dispatchCommand(CONTROLLED_TEXT_INSERTION_COMMAND, character);
		}
	});
}

/** Reproduce a key pressed before React paints Lexical's latest text update. */
export async function typeAndPressInLexicalEditor(
	field: HTMLElement,
	text: string,
	key: string,
): Promise<void> {
	const editor = lexicalEditor(field);
	await act(async () => {
		field.focus();
		editor.update(() => $getRoot().selectEnd(), { discrete: true });
		for (const character of text) {
			editor.dispatchCommand(CONTROLLED_TEXT_INSERTION_COMMAND, character);
		}
		field.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }));
	});
}

export async function placeLexicalCaret(field: HTMLElement, offset: number): Promise<void> {
	const editor = lexicalEditor(field);
	await act(async () => {
		editor.update(
			() => {
				const node = $getRoot().getFirstDescendant();
				if (!$isTextNode(node)) throw new Error("Expected composer text");
				node.select(offset, offset);
			},
			{ discrete: true },
		);
	});
}

export function lexicalEditorText(field: HTMLElement): string {
	return lexicalEditor(field)
		.getEditorState()
		.read(() => $getRoot().getChildren().map((child) => child.getTextContent()).join("\n"));
}
