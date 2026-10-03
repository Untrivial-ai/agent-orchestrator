import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";

const gets = vi.hoisted(() => new Map<string, ReturnType<typeof vi.fn>>());
vi.mock("../lib/host-clients", () => ({ clientForHost: (hostId: string) => ({ GET: gets.get(hostId) }) }));

import { RemoteDirectoryPicker } from "./RemoteDirectoryPicker";

beforeEach(() => gets.clear());

it("browses the selected host's filesystem even when hosts contain the same path", async () => {
	const a = vi.fn(async () => ({ data: { path: "/srv/todo-app", parent: "/srv", entries: [], truncated: false } }));
	const b = vi.fn(async () => ({ data: { path: "/srv/todo-app", parent: "/srv", entries: [], truncated: false } }));
	gets.set("host-a", a);
	gets.set("host-b", b);
	const onChoose = vi.fn();
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const view = render(<QueryClientProvider client={client}><RemoteDirectoryPicker hostId="host-a" hostLabel="Host A" connected initialPath="/srv/todo-app" onChoose={onChoose} onClose={vi.fn()} /></QueryClientProvider>);
	await waitFor(() => expect(a).toHaveBeenCalledWith("/api/v1/fs/dirs", { params: { query: { path: "/srv/todo-app" } } }));
	await userEvent.click(await screen.findByRole("button", { name: "Use this folder" }));
	expect(onChoose).toHaveBeenCalledWith("/srv/todo-app");
	view.unmount();
	render(<QueryClientProvider client={client}><RemoteDirectoryPicker hostId="host-b" hostLabel="Host B" connected initialPath="/srv/todo-app" onChoose={onChoose} onClose={vi.fn()} /></QueryClientProvider>);
	await waitFor(() => expect(b).toHaveBeenCalledWith("/api/v1/fs/dirs", { params: { query: { path: "/srv/todo-app" } } }));
	expect(a).toHaveBeenCalledTimes(1);
});

it("never calls a disconnected host or selects cached folders", async () => {
	const get = vi.fn();
	gets.set("host-a", get);
	const onChoose = vi.fn();
	render(<QueryClientProvider client={new QueryClient()}><RemoteDirectoryPicker hostId="host-a" hostLabel="Host A" connected={false} onChoose={onChoose} onClose={vi.fn()} /></QueryClientProvider>);
	expect(screen.getByRole("button", { name: "Use this folder" })).toBeDisabled();
	expect(get).not.toHaveBeenCalled();
	expect(onChoose).not.toHaveBeenCalled();
});
