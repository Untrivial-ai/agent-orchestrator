import { randomUUID } from "node:crypto";
import { mkdir, readFile, rename, rm, stat, writeFile } from "node:fs/promises";
import path from "node:path";
import { isBrowserProfileId } from "../shared/browser-profiles";
import {
	BROWSER_SITE_PERMISSIONS, browserSiteOrigin, defaultBrowserSitePermissions,
	type BrowserSitePermission, type BrowserSitePermissions, type BrowserSitePermissionSetting,
} from "../shared/browser-site-settings";

type SiteRecord = { scope: string; origin: string; permissions: BrowserSitePermissions };

export class BrowserSiteSettingsStore {
	private records = new Map<string, SiteRecord>();
	private queue = Promise.resolve();
	private readonly file: string;
	constructor(private readonly stateDir: string) {
		this.file = path.join(stateDir, "browser-site-settings.json");
	}

	async initialize(): Promise<void> {
		try {
			if ((await stat(this.file)).size > 4 * 1024 * 1024) throw new Error("Site settings exceed the size limit");
			const data = JSON.parse(await readFile(this.file, "utf8"));
			if (data.version !== 1 || !Array.isArray(data.sites) || data.sites.length > 10000) throw new Error("Invalid site settings");
			for (const site of data.sites) {
				if (!site || !isBrowserProfileId(site.scope) || !browserSiteOrigin(site.origin) || browserSiteOrigin(site.origin) !== site.origin ||
					!site.permissions || !BROWSER_SITE_PERMISSIONS.every((permission) => ["allow", "ask", "block"].includes(site.permissions[permission]))) {
					throw new Error("Invalid site settings");
				}
				this.records.set(`${site.scope}\n${site.origin}`, site);
			}
		} catch (error) {
			this.records.clear();
			if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
		}
	}

	get(scope: string, origin: string): BrowserSitePermissions {
		return { ...(this.records.get(`${scope}\n${origin}`)?.permissions ?? defaultBrowserSitePermissions()) };
	}

	private change(operation: (next: Map<string, SiteRecord>) => void): Promise<void> {
		const run = this.queue.then(async () => {
			const next = new Map(this.records);
			operation(next);
			if (next.size > 10000) throw new Error("Site settings exceed the entry limit");
			// Temporary partitions remain in memory only.
			const sites = [...next.values()].filter((site) => isBrowserProfileId(site.scope));
			const serialized = JSON.stringify({ version: 1, sites });
			const previous = JSON.stringify({ version: 1, sites: [...this.records.values()].filter((site) => isBrowserProfileId(site.scope)) });
			if (serialized === previous) {
				this.records = next;
				return;
			}
			if (Buffer.byteLength(serialized) > 4 * 1024 * 1024) throw new Error("Site settings exceed the size limit");
			await mkdir(this.stateDir, { recursive: true });
			const temporary = `${this.file}.${randomUUID()}.tmp`;
			try {
				await writeFile(temporary, serialized, { mode: 0o600 });
				await rename(temporary, this.file);
				this.records = next;
			} catch (error) {
				await rm(temporary, { force: true }).catch(() => undefined);
				throw error;
			}
		});
		this.queue = run.catch(() => undefined);
		return run;
	}

	set(scope: string, origin: string, permission: BrowserSitePermission, setting: BrowserSitePermissionSetting): Promise<void> {
		if (browserSiteOrigin(origin) !== origin || !BROWSER_SITE_PERMISSIONS.includes(permission) || !["allow", "ask", "block"].includes(setting)) {
			return Promise.reject(new Error("Invalid site permission"));
		}
		return this.change((next) => next.set(`${scope}\n${origin}`, {
			scope, origin, permissions: { ...this.get(scope, origin), [permission]: setting },
		}));
	}

	reset(scope: string, origin?: string): Promise<void> {
		return this.change((next) => {
			for (const [key, site] of next) if (site.scope === scope && (!origin || site.origin === origin)) next.delete(key);
		});
	}
}
