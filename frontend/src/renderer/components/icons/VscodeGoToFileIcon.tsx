import type { SVGProps } from "react";

/** VS Code's Codicon `go-to-file`, used for opening a file in the main workspace. */
export function VscodeGoToFileIcon(props: SVGProps<SVGSVGElement>) {
	return (
		<svg fill="currentColor" viewBox="0 0 16 16" {...props}>
			<path
				clipRule="evenodd"
				d="M10.571 1.14 13.85 4.44l.15.36v9.7l-.5.5h-11l-.5-.5V8h1v6h10V6H9.5L9 5.5V2H8V1h2.22l.351.14ZM10 5h3l-3-3v3Z"
				fillRule="evenodd"
			/>
			<path d="M6.854 2.145v.707l-2 2-.707-.707L5.293 3H2.5a1.5 1.5 0 1 0 0 3v1a2.5 2.5 0 0 1 0-5h2.793L4.147.853 4.854.146l2 2Z" />
		</svg>
	);
}
