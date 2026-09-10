import type { Root } from "hast";
import type { GrammarName } from "./code-highlight";
import { createEngine } from "./code-highlight-engine";

const engine = createEngine();
self.onmessage = (event: MessageEvent<{ id: number; code: string; language: GrammarName }>) => {
	const { id, code, language } = event.data;
	let tree: Root | undefined;
	try {
		tree = engine.highlight(language, code);
	} catch {
		// An unsupported grammar or parser failure keeps the caller's plain text.
	}
	self.postMessage({ id, tree });
};
