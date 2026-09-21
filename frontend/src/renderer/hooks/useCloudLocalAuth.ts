/**
 * Dev-only local (email/password) cloud sign-in for the renderer.
 *
 * Whether the surface is offered is decided authoritatively in the main process
 * (unpackaged/dev build AND a loopback control plane); this hook only mirrors
 * that decision for UI visibility and forwards the credentials over the bridge.
 * On success the main process pushes the new session over cloud:sessionChanged,
 * so useCloudSession flips to "authenticated" on its own — no manual refresh.
 */

import { useEffect, useState } from "react";
import type { CloudAccount } from "../../shared/cloud-account";
import { aoBridge } from "../lib/bridge";
import { useSettings } from "./useSettings";

export interface LocalRegisterFields {
	email: string;
	displayName: string;
	password: string;
	orgSlug: string;
	orgName: string;
}

export interface LocalLoginFields {
	email: string;
	password: string;
}

export interface UseCloudLocalAuthResult {
	/** Whether the dev-only local sign-in surface should be shown. */
	available: boolean;
	/** Resolved loopback control-plane base URL the credentials are sent to. */
	cpUrl: string;
	register: (fields: LocalRegisterFields) => Promise<CloudAccount>;
	login: (fields: LocalLoginFields) => Promise<CloudAccount>;
}

export function useCloudLocalAuth(): UseCloudLocalAuthResult {
	const { settings } = useSettings();
	const cpUrl = settings?.cloudControlPlaneUrl ?? "";
	const [available, setAvailable] = useState(false);

	useEffect(() => {
		let active = true;
		if (cpUrl === "") {
			setAvailable(false);
			return;
		}
		aoBridge.cloud
			.localAuthAvailable(cpUrl)
			.then((ok) => {
				if (active) setAvailable(ok);
			})
			.catch(() => {
				if (active) setAvailable(false);
			});
		return () => {
			active = false;
		};
	}, [cpUrl]);

	return {
		available,
		cpUrl,
		register: (fields) => aoBridge.cloud.localRegister({ cpUrl, ...fields }),
		login: (fields) => aoBridge.cloud.localLogin({ cpUrl, ...fields }),
	};
}
