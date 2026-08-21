import { useState } from "react";
import { cn } from "./utils";

export type GithubAvatarProps = {
	login: string;
	avatarUrl?: string;
	className?: string;
};

function initials(login: string): string {
	return login
		.replace(/^@/, "")
		.trim()
		.split(/[-_\s]+/)
		.filter(Boolean)
		.slice(0, 2)
		.map((part) => part[0]?.toUpperCase() ?? "")
		.join("") || "?";
}

export function GithubAvatar({ login, avatarUrl, className }: GithubAvatarProps) {
	const normalizedLogin = login.replace(/^@/, "").trim();
	const [loadedUrl, setLoadedUrl] = useState<string>();
	const normalizedAvatarUrl = avatarUrl?.trim() ?? "";
	const loaded = loadedUrl === normalizedAvatarUrl;

	return (
		<span
			aria-hidden="true"
			className={cn("relative inline-flex size-icon-sm shrink-0 items-center justify-center overflow-hidden rounded-full bg-muted text-micro font-semibold text-muted-foreground", className)}
		>
			{loaded ? null : initials(normalizedLogin)}
			{normalizedAvatarUrl ? (
				<img
					alt=""
					className={cn("absolute inset-0 size-full object-cover", loaded ? "opacity-100" : "opacity-0")}
					draggable={false}
					loading="lazy"
					onError={() => setLoadedUrl(undefined)}
					onLoad={() => setLoadedUrl(normalizedAvatarUrl)}
					referrerPolicy="no-referrer"
					src={normalizedAvatarUrl}
				/>
			) : null}
		</span>
	);
}
