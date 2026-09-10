type InjectableTerminalRenderer = {
	injectJavaScript: (script: string) => void;
};

export function adjustTerminalViewport(renderer: InjectableTerminalRenderer | null, direction: -1 | 1): boolean {
	if (!renderer) return false;
	renderer.injectJavaScript(`window.__aoAdjustTerminalZoom(${direction}); true;`);
	return true;
}
