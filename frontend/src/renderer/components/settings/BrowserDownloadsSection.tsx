import { Search } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useBrowserDownloads } from "../../hooks/useBrowserDownloads";
import { BrowserDownloadsList } from "../BrowserDownloadsList";
import { Button } from "../ui/button";
import { SettingsSection } from "./SettingsSection";

export function BrowserDownloadsSection({ titleHidden }: { titleHidden?: boolean }) {
	const { t } = useTranslation();
	const { downloads, error, action, clear } = useBrowserDownloads();
	const [query, setQuery] = useState("");
	const filtered = useMemo(() => {
		const normalized = query.trim().toLocaleLowerCase();
		return normalized ? downloads.filter((download) => download.fileName.toLocaleLowerCase().includes(normalized)) : downloads;
	}, [downloads, query]);
	const hasFinished = downloads.some((download) => download.status !== "progressing" && download.status !== "paused");

	return (
		<SettingsSection title={t("settings.downloads")} sectionId="downloads" titleHidden={titleHidden}>
			<div className="flex items-center gap-2">
				<label className="flex h-control-form min-w-0 flex-1 items-center gap-2 rounded-md border border-(--color-border-settings-input) bg-(--color-bg-settings-input) px-3">
					<Search aria-hidden="true" className="size-4 shrink-0 text-settings-muted" />
					<input
						aria-label={t("browser.downloads.search")}
						className="h-full min-w-0 flex-1 bg-transparent text-sm text-settings-label outline-none placeholder:text-settings-muted"
						onChange={(event) => setQuery(event.target.value)}
						placeholder={t("browser.downloads.search")}
						value={query}
					/>
				</label>
				<Button
					className="text-settings-muted hover:text-settings-label"
					disabled={!hasFinished}
					onClick={() => void clear()}
					type="button"
					variant="ghost"
				>
					{t("browser.downloads.clearAll")}
				</Button>
			</div>
			<BrowserDownloadsList downloads={filtered} error={error} onAction={(id, nextAction) => void action(id, nextAction)} />
		</SettingsSection>
	);
}
