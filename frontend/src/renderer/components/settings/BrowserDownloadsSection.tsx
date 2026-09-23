import { Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useBrowserDownloads } from "../../hooks/useBrowserDownloads";
import { BrowserDownloadsList } from "../BrowserDownloadsList";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
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
				<Input aria-label={t("browser.downloads.search")} onChange={(event) => setQuery(event.target.value)} placeholder={t("browser.downloads.search")} value={query} />
				<Button disabled={!hasFinished} onClick={() => void clear()} type="button" variant="outline">
					<Trash2 aria-hidden="true" className="size-icon-base" />
					{t("browser.downloads.clearAll")}
				</Button>
			</div>
			<div className="mt-3">
				<BrowserDownloadsList downloads={filtered} error={error} onAction={(id, nextAction) => void action(id, nextAction)} />
			</div>
		</SettingsSection>
	);
}
