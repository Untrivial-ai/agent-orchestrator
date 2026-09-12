import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { Button } from "./ui/button";

type Result = components["schemas"]["SessionImportSearchResult"];
type Destination = components["schemas"]["SessionImportDestination"];
type Page = components["schemas"]["SessionImportSearchPage"];

export function FindSessionToImport({
	initialQuery,
	onOpen,
	onPendingChange,
}: {
	initialQuery: string;
	onOpen: (projectId: string, sessionId: string) => void;
	onPendingChange?: (pending: boolean) => void;
}) {
	const { t, i18n } = useTranslation();
	const [query, setQuery] = useState(initialQuery);
	const [page, setPage] = useState<Page>();
	const [selected, setSelected] = useState("");
	const [previewId, setPreviewId] = useState<string>();
	const [destination, setDestination] = useState<Destination>();
	const [folder, setFolder] = useState("");
	const [error, setError] = useState("");
	const [loading, setLoading] = useState(false);
	const [pending, setPending] = useState(false);
	const [revision, setRevision] = useState(0);
	const generation = useRef(0);
	const previewGeneration = useRef(0);
	const searchAbort = useRef<AbortController | undefined>(undefined);
	const previewAbort = useRef<AbortController | undefined>(undefined);
	const busy = useRef(false);
	const alive = useRef(true);
	const input = useRef<HTMLInputElement>(null);
	const [pageCursor, setPageCursor] = useState<string>();
	const confirmButton = useRef<HTMLButtonElement>(null);
	useEffect(() => {
		confirmButton.current?.focus();
	}, [destination]);
	useEffect(() => {
		onPendingChange?.(pending);
		return () => onPendingChange?.(false);
	}, [pending, onPendingChange]);

	useEffect(() => {
		alive.current = true;
		const controller = new AbortController();
		void apiClient
			.POST("/api/v1/session-import/refresh", { signal: controller.signal })
			.then(({ data, error: failure }) => {
				if (controller.signal.aborted) return;
				if (failure) setError(apiErrorMessage(failure, t("command.failed")));
				if (data) setRevision((value) => value + 1);
			})
			.catch((failure: unknown) => {
				if (!controller.signal.aborted) setError(String(failure));
			});
		return () => {
			alive.current = false;
			controller.abort();
			searchAbort.current?.abort();
			previewAbort.current?.abort();
			previewGeneration.current++;
			generation.current++;
		};
	}, [t]);

	useEffect(() => {
		const current = ++generation.current;
		const controller = new AbortController();
		searchAbort.current?.abort();
		searchAbort.current = controller;
		setLoading(true);
		const timer = window.setTimeout(() => {
			void apiClient
				.GET("/api/v1/session-import/search", {
					params: { query: { query, limit: 50, cursor: pageCursor } },
					signal: controller.signal,
				})
				.then(({ data, error: failure }) => {
					if (current !== generation.current || controller.signal.aborted)
						return;
					if (failure)
						throw new Error(apiErrorMessage(failure, t("command.failed")));
					if (data) {
						setPage(data);
						setSelected((id) =>
							data.results.some((row) => row.id === id)
								? id
								: (data.results[0]?.id ?? ""),
						);
					}
				})
				.catch((failure: unknown) => {
					if (current === generation.current && !controller.signal.aborted)
						setError(String(failure));
				})
				.finally(() => {
					if (current === generation.current && !controller.signal.aborted)
						setLoading(false);
				});
		}, 150);
		return () => {
			window.clearTimeout(timer);
			controller.abort();
		};
	}, [query, pageCursor, revision, t]);

	useEffect(() => {
		if (!page?.status.running) return;
		const timer = window.setTimeout(
			() => setRevision((value) => value + 1),
			1500,
		);
		return () => window.clearTimeout(timer);
	}, [page]);

	async function preview(id: string, locateFolder = "", preserveError = false) {
		const current = ++previewGeneration.current;
		previewAbort.current?.abort();
		const controller = new AbortController();
		previewAbort.current = controller;
		setPreviewId(id);
		setDestination(undefined);
		if (!preserveError) setError("");
		try {
			const { data, error: failure } = await apiClient.GET(
				"/api/v1/session-import/search/{resultId}/destination",
				{
					params: {
						path: { resultId: id },
						query: { locateFolder: locateFolder || undefined },
					},
					signal: controller.signal,
				},
			);
			if (current !== previewGeneration.current || controller.signal.aborted)
				return;
			if (failure)
				throw new Error(apiErrorMessage(failure, t("command.failed")));
			setDestination(data);
		} catch (failure) {
			if (current === previewGeneration.current && !controller.signal.aborted)
				setError(String(failure));
		}
	}
	async function confirm() {
		if (!destination || busy.current) return;
		if (destination.action === "open") {
			if (destination.projectId && destination.sessionId)
				onOpen(destination.projectId, destination.sessionId);
			return;
		}
		if (!destination.confirmationToken) return;
		busy.current = true;
		setPending(true);
		setError("");
		try {
			const { data, error: failure } = await apiClient.POST(
				"/api/v1/session-import/search/{resultId}/import",
				{
					params: { path: { resultId: destination.id } },
					body: {
						confirmationToken: destination.confirmationToken,
						addProject: destination.action === "add_project",
						locateFolder: folder || undefined,
					},
				},
			);
			if (!alive.current) return;
			if (failure)
				throw new Error(apiErrorMessage(failure, t("command.failed")));
			if (data?.error)
				throw new Error(
					data.projectId
						? `${t("importSearch.projectRetained")} ${data.error}`
						: data.error,
				);
			if (!data?.projectId || !data.sessionId)
				throw new Error(t("command.failed"));
			onOpen(data.projectId, data.sessionId);
		} catch (failure) {
			if (alive.current) {
				setError(String(failure));
				await preview(destination.id, folder, true);
			}
		} finally {
			busy.current = false;
			if (alive.current) setPending(false);
		}
	}
	const rows = page?.results ?? [];
	return (
		<div
			onKeyDown={(event) => event.stopPropagation()}
			className="p-3 space-y-3"
		>
			{previewId ? (
				<>
					<Button
						variant="ghost"
						disabled={pending}
						onClick={() => {
							previewGeneration.current++;
							previewAbort.current?.abort();
							setPreviewId(undefined);
							setDestination(undefined);
							setFolder("");
							setError("");
						}}
					>
						{t("importSearch.results")}
					</Button>
					{destination ? (
						<div className="space-y-3">
							<h2 className="text-sm font-medium whitespace-pre-wrap break-words">
								{destination.title}
							</h2>
							<p className="text-xs text-muted-foreground">
								{destination.provider === "claude-code"
									? "Claude Code"
									: destination.provider === "codex"
										? "Codex"
										: destination.provider}
							</p>
							<p className="text-xs break-all">
								{destination.path ?? destination.sourceCwd}
							</p>
							{destination.reason && (
								<p className="text-xs">{destination.reason}</p>
							)}
							{destination.action === "unavailable" ? (
								<form
									className="space-y-2"
									onSubmit={(event) => {
										event.preventDefault();
										void preview(previewId, folder);
									}}
								>
									<label className="text-xs" htmlFor="import-folder">
										{t("importSearch.locate")}
									</label>
									<input
										id="import-folder"
										className="w-full rounded border p-2 text-sm"
										value={folder}
										onChange={(event) => setFolder(event.target.value)}
									/>
									<Button type="submit" disabled={!folder.trim()}>
										{t("importSearch.checkFolder")}
									</Button>
								</form>
							) : (
								<Button
									ref={confirmButton}
									disabled={
										pending ||
										(destination.action !== "open" &&
											!destination.confirmationToken)
									}
									onClick={() => void confirm()}
								>
									{pending
										? t("importSearch.importing")
										: destination.action === "open"
											? t("command.open")
											: destination.action === "add_project"
												? t("importSearch.addProject")
												: t("importSearch.import")}
								</Button>
							)}
						</div>
					) : (
						!error && <p role="status">{t("importSearch.loading")}</p>
					)}
					{!destination && error && (
						<Button onClick={() => void preview(previewId, folder)}>
							{t("importSearch.retry")}
						</Button>
					)}
				</>
			) : (
				<>
					<input
						ref={input}
						autoFocus
						aria-label={t("importSearch.title")}
						placeholder={t("importSearch.placeholder")}
						value={query}
						className="w-full bg-transparent p-2 text-sm outline-none"
						onChange={(event) => {
							generation.current++;
							searchAbort.current?.abort();
							setQuery(event.target.value);
							setPageCursor(undefined);
							setError("");
						}}
						onKeyDown={(event) => {
							if (event.nativeEvent.isComposing) return;
							if (event.key === "ArrowDown" || event.key === "ArrowUp") {
								event.preventDefault();
								const index = rows.findIndex((row) => row.id === selected);
								const next =
									rows[
										(index +
											(event.key === "ArrowDown" ? 1 : -1) +
											rows.length) %
											rows.length
									];
								if (next) {
									setSelected(next.id);
									document
										.getElementById(`import-result-${next.id}`)
										?.scrollIntoView({ block: "nearest" });
								}
							}
							if (event.key === "Enter" && selected) {
								event.preventDefault();
								void preview(selected);
							}
						}}
						role="combobox"
						aria-expanded="true"
						aria-controls="import-results"
						aria-activedescendant={
							selected ? `import-result-${selected}` : undefined
						}
					/>
					<div
						id="import-results"
						role="listbox"
						aria-label={t("importSearch.results")}
						className="max-h-80 overflow-y-auto"
					>
						{rows.map((row: Result) => (
							<button
								type="button"
								role="option"
								aria-selected={selected === row.id}
								id={`import-result-${row.id}`}
								key={row.id}
								className={`block w-full rounded p-3 text-left ${selected === row.id ? "bg-surface" : ""}`}
								onFocus={() => setSelected(row.id)}
								onClick={() => void preview(row.id)}
							>
								<span className="block text-sm font-medium whitespace-pre-wrap break-words">
									{row.title}
								</span>
								<span className="block text-xs text-muted-foreground">
									{row.provider === "claude-code"
										? "Claude Code"
										: row.provider === "codex"
											? "Codex"
											: row.provider}{" "}
									·{" "}
									{new Date(row.lastActivity).toLocaleDateString(
										i18n.resolvedLanguage,
									)}
									{row.sessionId ? ` · ${t("command.open")}` : ""}
								</span>
								{row.folderHint &&
									rows.filter((other) => other.title === row.title).length >
										1 && (
										<span className="block text-xs text-muted-foreground break-all">
											{row.folderHint}
										</span>
									)}
							</button>
						))}
					</div>
					{loading && (
						<p role="status" className="text-xs">
							{t("importSearch.loading")}
						</p>
					)}
					{!loading && page && !rows.length && (
						<p className="text-sm">{t("importSearch.empty")}</p>
					)}
					{page?.nextCursor && (
						<Button
							variant="ghost"
							disabled={loading}
							onClick={() => setPageCursor(page.nextCursor)}
						>
							{t("importSearch.more")}
						</Button>
					)}
				</>
			)}
			{page?.status.running && (
				<p role="status" className="text-xs text-muted-foreground">
					{t("importSearch.indexing", { count: page.status.scanned })}
				</p>
			)}
			{!!page?.status.errors.length && (
				<div role="status" className="text-xs">
					<p>{t("importSearch.partial")}</p>
					{page.status.errors.map((message, index) => (
						<p key={index}>{message}</p>
					))}
				</div>
			)}
			{error && (
				<p role="alert" className="text-xs text-destructive break-words">
					{error}
				</p>
			)}
		</div>
	);
}
