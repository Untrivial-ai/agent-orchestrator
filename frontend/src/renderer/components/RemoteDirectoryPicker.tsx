import * as Dialog from "@radix-ui/react-dialog";
import { useQuery } from "@tanstack/react-query";
import { ArrowUp, ChevronRight, Folder, FolderGit2, X } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { apiErrorMessage } from "../lib/api-client";
import { clientForHost } from "../lib/host-clients";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Skeleton } from "./ui/skeleton";

/** The only host-specific part of the shared create-project flow: browse the daemon's filesystem. */
export function RemoteDirectoryPicker({ hostId, hostLabel, connected, initialPath = "", onChoose, onClose }: {
	hostId: string;
	hostLabel: string;
	connected: boolean;
	initialPath?: string;
	onChoose: (path: string) => void;
	onClose: () => void;
}) {
	const { t } = useTranslation();
	const [path, setPath] = useState(initialPath);
	const [lookup, setLookup] = useState(initialPath);
	const directory = useQuery({
		queryKey: ["remote-fs-dirs", hostId, lookup],
		enabled: connected,
		retry: false,
		queryFn: async () => {
			const { data, error } = await clientForHost(hostId).GET("/api/v1/fs/dirs", { params: { query: lookup ? { path: lookup } : {} } });
			if (error || !data) throw new Error(apiErrorMessage(error, t("remote.directoryPicker.listFailed")));
			return data;
		},
	});
	return <Dialog.Root open onOpenChange={(open) => { if (!open) onClose(); }}>
		<Dialog.Portal>
			<Dialog.Overlay className="dialog-overlay" />
			<Dialog.Content className="fixed left-1/2 top-1/2 z-overlay flex max-h-[min(640px,calc(100dvh-24px))] w-[min(560px,calc(100vw-24px))] -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-lg border border-border bg-popover text-popover-foreground shadow-xl">
				<div className="flex items-center gap-3 border-b border-border px-4 py-3">
					<Dialog.Title className="min-w-0 flex-1 truncate text-balance text-lg font-semibold">{t("remote.directoryPicker.title", { host: hostLabel })}</Dialog.Title>
					<Dialog.Description className="sr-only">{t("remote.directoryPicker.description")}</Dialog.Description>
					<Button type="button" variant="ghost" size="icon-sm" aria-label={t("remote.directoryPicker.close")} onClick={onClose}><X className="size-4" aria-hidden="true" /></Button>
				</div>
				<form className="flex gap-2 p-4" onSubmit={(event) => { event.preventDefault(); setLookup(path.trim()); }}>
					<Input aria-label={t("remote.directoryPicker.path")} value={path} onChange={(event) => setPath(event.target.value)} disabled={!connected} placeholder="/home/you/code" />
					<Button type="submit" variant="outline" disabled={!connected}>{t("remote.directoryPicker.go")}</Button>
				</form>
				<div className="flex items-center gap-2 border-y border-border px-4 py-2">
					<Button type="button" variant="ghost" size="icon-sm" aria-label={t("remote.directoryPicker.parent")} disabled={!connected || !directory.data || directory.data.parent === directory.data.path} onClick={() => { if (directory.data) { setPath(directory.data.parent); setLookup(directory.data.parent); } }}><ArrowUp className="size-4" aria-hidden="true" /></Button>
					<span className="min-w-0 flex-1 truncate font-mono text-xs text-muted-foreground" title={directory.data?.path}>{directory.data?.path ?? t("remote.directoryPicker.current")}</span>
					<Button type="button" variant="primary" size="sm" disabled={!connected || !directory.data} onClick={() => directory.data && onChoose(directory.data.path)}>{t("remote.directoryPicker.use")}</Button>
				</div>
				<div className="min-h-20 overflow-y-auto p-2">
					{!connected ? <p role="alert" className="p-3 text-sm text-destructive">{t("remote.directoryPicker.connect", { host: hostLabel })}</p> : directory.isPending ? <div role="status" aria-label={t("remote.directoryPicker.loading")} className="space-y-2 p-2"><Skeleton className="h-7 w-full" /><Skeleton className="h-7 w-4/5" /></div> : directory.isError ? <div className="flex items-center justify-between gap-2 p-3"><p role="alert" className="text-sm text-destructive">{directory.error.message}</p><Button type="button" variant="outline" size="sm" onClick={() => void directory.refetch()}>{t("remote.directoryPicker.retry")}</Button></div> : directory.data?.entries.length ? directory.data.entries.map((entry) => <button key={entry.path} type="button" className="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm hover:bg-muted focus-visible:outline-2 focus-visible:outline-primary" onClick={() => { setPath(entry.path); setLookup(entry.path); }}>
						{entry.gitRepo ? <FolderGit2 className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" /> : <Folder className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />}
						<span className="min-w-0 flex-1 truncate">{entry.name}</span><ChevronRight className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
					</button>) : <p className="p-3 text-sm text-muted-foreground">{t("remote.directoryPicker.empty")}</p>}
					{directory.data?.truncated && <p className="px-2 py-1 text-xs text-muted-foreground">{t("remote.directoryPicker.truncated")}</p>}
				</div>
			</Dialog.Content>
		</Dialog.Portal>
	</Dialog.Root>;
}
