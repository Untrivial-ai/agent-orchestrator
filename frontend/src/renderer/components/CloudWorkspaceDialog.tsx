import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { aoBridge } from "../lib/bridge";
import { setCloudApiBaseUrl } from "../lib/api-client";
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "./ui/dialog";

const DEFAULT_REPOSITORY =
	import.meta.env.VITE_AO_CLOUD_POC_REPOSITORY_URL ||
	"https://github.com/Untrivial-ai/agent-orchestrator.git";
const DEFAULT_REF = import.meta.env.VITE_AO_CLOUD_POC_REPOSITORY_REF || "main";

export function CloudWorkspaceDialog({
	open,
	onOpenChange,
	onConnected,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	onConnected: () => void;
}) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const navigate = useNavigate();
	const [repositoryUrl, setRepositoryUrl] = useState(DEFAULT_REPOSITORY);
	const [repositoryRef, setRepositoryRef] = useState(DEFAULT_REF);
	const [status, setStatus] = useState("");
	const [error, setError] = useState("");
	const [busy, setBusy] = useState(false);

	const createWorkspace = async () => {
		setBusy(true);
		setError("");
		setStatus(t("cloudWorkspace.requesting"));
		try {
			let response = await aoBridge.cloud.createWorkspace({
				repositoryUrl: repositoryUrl.trim(),
				repositoryRef: repositoryRef.trim() || undefined,
			});
			setStatus(t("cloudWorkspace.installing"));
			const deadline = Date.now() + 20 * 60 * 1000;
			while (response.workspace.state !== "ready") {
				if (response.workspace.state === "failed") {
					throw new Error(response.workspace.error || t("cloudWorkspace.provisioningFailed"));
				}
				if (Date.now() >= deadline) throw new Error(t("cloudWorkspace.provisioningTimedOut"));
				await new Promise((resolve) => window.setTimeout(resolve, 3_000));
				response = await aoBridge.cloud.getWorkspace({
					orgId: response.workspace.orgId,
					workspaceId: response.workspace.id,
				});
			}
			if (!response.previewUrl) throw new Error(t("cloudWorkspace.missingConnectionUrl"));
			setCloudApiBaseUrl(response.previewUrl);
			queryClient.clear();
			onConnected();
			onOpenChange(false);
			await navigate({ to: "/" });
		} catch (cause) {
			setError(cause instanceof Error ? cause.message : t("cloudWorkspace.createFailed"));
			setStatus("");
		} finally {
			setBusy(false);
		}
	};

	return (
		<Dialog open={open} onOpenChange={(next) => !busy && onOpenChange(next)}>
			<DialogContent className="w-[min(520px,calc(100vw-32px))]" showCloseButton={!busy}>
				<DialogHeader>
					<DialogTitle>{t("cloudWorkspace.title")}</DialogTitle>
					<DialogDescription>
						{t("cloudWorkspace.description")}
					</DialogDescription>
				</DialogHeader>
				<label className="grid gap-1.5 text-sm font-medium">
					{t("cloudWorkspace.repositoryUrl")}
					<input
						className="h-9 rounded-md border border-border bg-background px-3 font-normal outline-none focus:border-accent"
						disabled={busy}
						onChange={(event) => setRepositoryUrl(event.target.value)}
						value={repositoryUrl}
					/>
				</label>
				<label className="grid gap-1.5 text-sm font-medium">
					{t("cloudWorkspace.repositoryRef")}
					<input
						className="h-9 rounded-md border border-border bg-background px-3 font-normal outline-none focus:border-accent"
						disabled={busy}
						onChange={(event) => setRepositoryRef(event.target.value)}
						value={repositoryRef}
					/>
				</label>
				{status && <p className="text-sm text-muted-foreground">{status}</p>}
				{error && <p role="alert" className="text-sm text-destructive">{error}</p>}
				<DialogFooter className="flex-row justify-end">
					<button
						className="h-9 rounded-md border border-border px-3 text-sm"
						disabled={busy}
						onClick={() => onOpenChange(false)}
						type="button"
					>
						{t("createProject.cancel")}
					</button>
					<button
						className="h-9 rounded-md bg-foreground px-3 text-sm text-background disabled:opacity-50"
						disabled={busy || !repositoryUrl.trim()}
						onClick={() => void createWorkspace()}
						type="button"
					>
						{busy ? t("cloudWorkspace.creating") : t("cloudWorkspace.create")}
					</button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
