import { useState, type ReactNode } from "react";
import { useWindowFullScreen } from "../hooks/useWindowFullScreen";
import { hidesShellTopbar } from "../lib/platform";
import { cn } from "../lib/utils";

export function SessionFilesPopOut({ children }: { children: (topbarHost: HTMLDivElement) => ReactNode }) {
	const [topbarHost, setTopbarHost] = useState<HTMLDivElement | null>(null);
	const isNativeFullScreen = useWindowFullScreen();
	const macWindowed = hidesShellTopbar() && !isNativeFullScreen;
	return <div className={cn("files-popout-overlay", macWindowed && "files-popout-overlay--mac-windowed")}>
		<div aria-hidden="true" className="files-popout-backdrop" />
		<div className={cn("files-popout-titlebar", macWindowed && "files-popout-titlebar--mac-windowed")} data-testid="files-popout-topbar" ref={setTopbarHost} />
		<div className="files-popout-frame">{topbarHost ? children(topbarHost) : null}</div>
	</div>;
}
