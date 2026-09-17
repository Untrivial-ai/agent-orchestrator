import { act, render, screen } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { useUpdateStatus } from "./useUpdateStatus";
import type { UpdateStatus } from "../../main/update-settings";

const { getStatus, onStatus } = vi.hoisted(() => ({ getStatus: vi.fn(), onStatus: vi.fn() }));
vi.mock("../lib/bridge", () => ({ aoBridge: { updates: { getStatus, onStatus } } }));

function Probe() {
	const status = useUpdateStatus();
	return <span data-testid="state">{`${status.state}:${status.checkedAt ?? "-"}`}</span>;
}

beforeEach(() => {
	getStatus.mockReset();
	onStatus.mockReset();
});

it("registers the listener before requesting the snapshot", () => {
	const order: string[] = [];
	onStatus.mockImplementation(() => {
		order.push("listen");
		return () => undefined;
	});
	getStatus.mockImplementation(() => {
		order.push("snapshot");
		return new Promise<UpdateStatus>(() => undefined);
	});
	render(<Probe />);
	// The other order loses any push that lands in the gap.
	expect(order).toEqual(["listen", "snapshot"]);
});

it("does not let a late snapshot overwrite a newer push", async () => {
	let emit: (s: UpdateStatus) => void = () => undefined;
	let resolveSnapshot: (s: UpdateStatus) => void = () => undefined;
	onStatus.mockImplementation((cb: (s: UpdateStatus) => void) => {
		emit = cb;
		return () => undefined;
	});
	getStatus.mockReturnValue(new Promise<UpdateStatus>((resolve) => { resolveSnapshot = resolve; }));

	render(<Probe />);
	// A completed check arrives first...
	act(() => emit({ state: "not-available", checkedAt: 2000 }));
	expect(screen.getByTestId("state")).toHaveTextContent("not-available:2000");

	// ...then the older mount-time snapshot finally resolves.
	await act(async () => {
		resolveSnapshot({ state: "idle", checkedAt: 1000 });
	});
	expect(screen.getByTestId("state")).toHaveTextContent("not-available:2000");
});

it("still seeds from the snapshot when no push has arrived", async () => {
	onStatus.mockReturnValue(() => undefined);
	getStatus.mockResolvedValue({ state: "downloaded", checkedAt: 42 });
	render(<Probe />);
	expect(await screen.findByText("downloaded:42")).toBeVisible();
});

it("reconciles missed progress without checking a remote feed and ignores late poll results", async () => {
	vi.useFakeTimers();
	let emit!: (s: UpdateStatus) => void;
	onStatus.mockImplementation((cb) => { emit = cb; return vi.fn(); });
	getStatus.mockResolvedValue({ state: "downloading", percent: 10 });
	const view = render(<Probe />);
	try {
		await act(async () => {});
		getStatus.mockResolvedValue({ state: "preparing", checkedAt: 100 });
		await act(async () => { await vi.advanceTimersByTimeAsync(3_000); });
		expect(screen.getByTestId("state")).toHaveTextContent("preparing:100");
		let resolve!: (s: UpdateStatus) => void;
		getStatus.mockReturnValue(new Promise<UpdateStatus>((done) => { resolve = done; }));
		await act(async () => { await vi.advanceTimersByTimeAsync(3_000); });
		act(() => emit({ state: "downloaded", checkedAt: 200 }));
		await act(async () => { resolve({ state: "downloading", checkedAt: 100 }); });
		expect(screen.getByTestId("state")).toHaveTextContent("downloaded:200");
		view.unmount();
		const calls = getStatus.mock.calls.length;
		await vi.advanceTimersByTimeAsync(3_000);
		expect(getStatus).toHaveBeenCalledTimes(calls);
	} finally {
		view.unmount();
		vi.useRealTimers();
	}
});

it("uses one status subscription and reconciles every mounted surface", async () => {
	vi.useFakeTimers();
	onStatus.mockReturnValue(vi.fn());
	getStatus.mockResolvedValue({ state: "idle" });
	function SettingsProbe() {
		const status = useUpdateStatus(undefined, true);
		return <span data-testid="settings-state">{status.state}</span>;
	}
	const view = render(<><Probe /><SettingsProbe /></>);
	try {
		await act(async () => {});
		expect(onStatus).toHaveBeenCalledTimes(1);
		expect(getStatus).toHaveBeenCalledTimes(1);
		getStatus.mockResolvedValue({ state: "preparing" });
		await act(async () => { await vi.advanceTimersByTimeAsync(3_000); });
		expect(screen.getByTestId("settings-state")).toHaveTextContent("preparing");
		expect(screen.getByTestId("state")).toHaveTextContent("preparing");
	} finally {
		view.unmount();
		vi.useRealTimers();
	}
});
