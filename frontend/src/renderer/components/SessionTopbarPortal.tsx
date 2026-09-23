import { createContext, useContext, useMemo, useState, type ComponentProps, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { cn } from "../lib/utils";

type SessionTopbarContextValue = {
	host: HTMLDivElement | null;
	setHost: (host: HTMLDivElement | null) => void;
};

const SessionTopbarContext = createContext<SessionTopbarContextValue | undefined>(undefined);

export function SessionTopbarProvider({ children }: { children: ReactNode }) {
	const [host, setHost] = useState<HTMLDivElement | null>(null);
	const value = useMemo(() => ({ host, setHost }), [host]);

	return <SessionTopbarContext.Provider value={value}>{children}</SessionTopbarContext.Provider>;
}

export function SessionTopbarHost({ className, ref, ...props }: ComponentProps<"div">) {
	const context = useContext(SessionTopbarContext);
	if (!context) throw new Error("SessionTopbarHost must be used within SessionTopbarProvider");

	return (
		<div
			className={cn("workspace-topbar-container", className)}
			ref={(element) => {
				context.setHost(element);
				if (typeof ref === "function") ref(element);
				else if (ref) ref.current = element;
			}}
			{...props}
		/>
	);
}

export function SessionTopbarPortal({ children }: { children: ReactNode }) {
	const context = useContext(SessionTopbarContext);
	if (!context) return children;
	if (!context.host) return null;
	return createPortal(children, context.host);
}
