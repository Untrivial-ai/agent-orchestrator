import type { ActiveProxy } from "./remote-proxy";
import type { RemoteEntry } from "./remotes-store";

export type ConnectedHostView = {
	hostId: string;
	label: string;
	url: string;
	base: string;
};

type StartProxy = (entry: RemoteEntry) => Promise<ActiveProxy>;

/** One loopback proxy per connected host. */
export class RemoteRegistry {
	private readonly live = new Map<string, { view: ConnectedHostView; proxy: ActiveProxy; password: string }>();
	// ponytail: one queue serializes five-host startup; split by host if connect latency becomes measurable.
	private tail: Promise<void> = Promise.resolve();
	private closing = false;

	constructor(private readonly start: StartProxy) {}

	private enqueue<T>(action: () => Promise<T>): Promise<T> {
		const result = this.tail.then(action, action);
		this.tail = result.then(() => undefined, () => undefined);
		return result;
	}

	connect(entry: RemoteEntry): Promise<ConnectedHostView> {
		if (this.closing) return Promise.reject(new Error("remote connections are closing"));
		return this.enqueue(async () => {
			if (!entry.hostId) throw new Error("remote host must be paired again to record its identity");
			const existing = this.live.get(entry.url);
			if (existing?.view.hostId === entry.hostId && existing.password === entry.password) return existing.view;
			for (const [url, connected] of this.live) {
				if (url !== entry.url && connected.view.hostId !== entry.hostId) continue;
				this.live.delete(url);
				await connected.proxy.close();
			}
			const proxy = await this.start(entry);
			const view = { hostId: entry.hostId, label: entry.label, url: entry.url, base: proxy.base };
			this.live.set(entry.url, { view, proxy, password: entry.password });
			return view;
		});
	}

	previewUrl(hostId: string, sessionId: string, sourceUrl: string): string {
		const connected = [...this.live.values()].find(({ view }) => view.hostId === hostId);
		if (!connected) throw new Error(`Host ${hostId} is not connected`);
		return connected.proxy.previewUrl(sessionId, sourceUrl);
	}

	resolvePreviewUrl(hostId: string, sessionId: string, viewedUrl: string): string {
		const connected = [...this.live.values()].find(({ view }) => view.hostId === hostId);
		if (!connected) throw new Error(`Host ${hostId} is not connected`);
		return connected.proxy.resolvePreviewUrl(sessionId, viewedUrl);
	}

	disconnect(url: string): Promise<void> {
		return this.enqueue(async () => {
			const entry = this.live.get(url);
			if (!entry) return;
			this.live.delete(url);
			await entry.proxy.close();
		});
	}

	closeAll(): Promise<void> {
		this.closing = true;
		return this.enqueue(async () => {
			const entries = [...this.live.values()];
			this.live.clear();
			await Promise.all(entries.map(({ proxy }) => proxy.close()));
		});
	}
}
