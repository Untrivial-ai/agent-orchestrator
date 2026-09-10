import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Bell, Loader2, Smartphone, Trash2 } from "lucide-react";
import { apiClient, apiErrorCode, apiErrorMessage } from "../../lib/api-client";
import { Switch } from "../ui/switch";

export const mobileDevicesQueryKey = ["mobile-devices"] as const;

/**
 * Error code the daemon returns from all three roster routes (list/mute/remove)
 * when the on-disk device registry (~/.ao/data/mobile/push-devices.json) failed
 * to load — e.g. it's corrupt. This is distinct from "you have no devices": an
 * unreadable registry must be surfaced explicitly, never rendered as the empty
 * state.
 */
const DEVICE_REGISTRY_UNAVAILABLE_CODE = "DEVICE_REGISTRY_UNAVAILABLE";

interface MobileDevice {
	installId: string;
	deviceName?: string;
	platform?: string;
	muted: boolean;
	live: boolean;
	notificationsEnabled: boolean;
	lastSeenAt: string;
}

class MobileDevicesQueryError extends Error {
	code?: string;

	constructor(message: string, code?: string) {
		super(message);
		this.code = code;
	}
}

export async function fetchDevices(): Promise<MobileDevice[]> {
	const { data, error } = await apiClient.GET("/api/v1/mobile/devices");
	if (error || !data) throw new MobileDevicesQueryError(apiErrorMessage(error), apiErrorCode(error));
	return data.devices as MobileDevice[];
}

// MobileDevicesSection lists every paired phone with whether its app is open right
// now, a per-device mute switch, and a remove action. Live status comes from the
// daemon's presence tracker, which is fed by each phone's own REST poll.
export function MobileDevicesSection() {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const [confirmingRemoval, setConfirmingRemoval] = useState<string | null>(null);

	const query = useQuery({
		queryKey: mobileDevicesQueryKey,
		queryFn: fetchDevices,
		refetchInterval: 3000,
	});

	const invalidate = () => {
		void queryClient.invalidateQueries({ queryKey: mobileDevicesQueryKey });
	};

	const mute = useMutation({
		mutationFn: async ({ installId, muted }: { installId: string; muted: boolean }) => {
			const { error } = await apiClient.PATCH("/api/v1/mobile/devices/{installId}", {
				params: { path: { installId } },
				body: { muted },
			});
			if (error) throw new Error(apiErrorMessage(error));
		},
		onSuccess: invalidate,
	});

	const remove = useMutation({
		mutationFn: async (installId: string) => {
			const { error } = await apiClient.DELETE("/api/v1/mobile/devices/{installId}", {
				params: { path: { installId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
		},
		onSuccess: () => {
			setConfirmingRemoval(null);
			invalidate();
		},
	});

	const devices = query.data ?? [];
	// Stable client-side order: the daemon sorts live-first then by LastSeenAt
	// descending, and LastSeenAt advances on every phone poll — with 2+ live
	// devices that ordering can flip on any 3s refetch, jumping rows under the
	// cursor mid-interaction (e.g. right as someone reaches for "Confirm remove").
	// installId never changes for a paired device, so sorting on it keeps row
	// order fixed across polls regardless of what the server returns.
	const sortedDevices = [...devices].sort((a, b) => a.installId.localeCompare(b.installId));
	const queryError = query.error as MobileDevicesQueryError | null;
	const registryUnavailable = queryError?.code === DEVICE_REGISTRY_UNAVAILABLE_CODE;
	// A transient poll failure (daemon restart mid-refetch, a one-off 500) should
	// not blank a list we already successfully loaded — that flickers the whole
	// section red and back every time. Only replace the section outright when
	// there is no retained data to show. DEVICE_REGISTRY_UNAVAILABLE always takes
	// over the section regardless of stale data — that state is distinct enough
	// (an unreadable on-disk registry) that showing a stale list next to it would
	// be misleading.
	const hasData = query.data !== undefined;

	// No paired devices (or still loading with nothing cached) → no section at
	// all. Errors and an unreadable registry still render so they stay visible.
	if (!registryUnavailable && !queryError && devices.length === 0) return null;
	const mutationError =
		(mute.error instanceof Error && mute.error.message) ||
		(remove.error instanceof Error && remove.error.message) ||
		null;

	return (
		<section className="mt-6">
			<h3 className="text-sm font-medium text-settings-label">{t("mobile.devices.title")}</h3>

			{query.isLoading ? (
				<div className="mt-3 flex items-center gap-2 text-caption text-settings-muted">
					<Loader2 className="size-3 animate-spin" /> {t("mobile.devices.loading")}
				</div>
			) : registryUnavailable ? (
				<p className="mt-3 text-caption text-error">{t("mobile.devices.registryUnavailable")}</p>
			) : queryError && !hasData ? (
				<p className="mt-3 text-caption text-error">{queryError.message}</p>
			) : devices.length === 0 ? (
				<p className="mt-3 text-caption text-settings-muted">{t("mobile.devices.empty")}</p>
			) : (
				<>
					{queryError && <p className="mt-3 text-caption text-error">{queryError.message}</p>}
					<ul className="mt-2 divide-y divide-[var(--color-border-settings-input)]">
						{sortedDevices.map((device) => {
							const name = device.deviceName || t("mobile.devices.unnamed");
							return (
								<li
									key={device.installId}
									className="flex min-h-12 items-center gap-3 py-2.5"
								>
									<Smartphone className="size-4 shrink-0 text-settings-muted" aria-hidden="true" />
									<div className="min-w-0 flex-1">
										<div className="truncate text-sm">{name}</div>
									</div>

									<div className="flex items-center gap-2" title={t("mobile.devices.notificationsFor", { name })}>
										<Bell className="size-4 text-settings-muted" aria-hidden="true" data-testid="bell" />
										<Switch
											checked={device.notificationsEnabled && !device.muted}
											disabled={mute.isPending || !device.notificationsEnabled}
											aria-label={t("mobile.devices.notificationsFor", { name })}
											onCheckedChange={(next) =>
												mute.mutate({ installId: device.installId, muted: !next })
											}
										/>
									</div>

									{confirmingRemoval === device.installId ? (
										<button
											type="button"
											className="min-h-10 px-1 text-caption text-error"
											disabled={remove.isPending}
											onClick={() => remove.mutate(device.installId)}
										>
											{t("mobile.devices.confirmRemove")}
										</button>
									) : (
										<button
											type="button"
											aria-label={t("mobile.devices.removeAria", { name })}
											className="grid size-10 place-items-center rounded-md text-settings-muted transition-colors hover:bg-interactive-hover hover:text-settings-label focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
											onClick={() => setConfirmingRemoval(device.installId)}
										>
											<Trash2 className="size-4" />
										</button>
									)}
								</li>
							);
						})}
					</ul>
				</>
			)}

			{mutationError && <p className="mt-2 text-caption text-error">{mutationError}</p>}
		</section>
	);
}
