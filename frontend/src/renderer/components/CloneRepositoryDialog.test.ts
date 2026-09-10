import React from "react";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import CloneRepositoryDialog, { joinCloneDestination, repositoryNameFromGitUrl, sameProjectPath } from "./CloneRepositoryDialog";

function cloneDialogProps(
	overrides: Partial<React.ComponentProps<typeof CloneRepositoryDialog>> = {},
): React.ComponentProps<typeof CloneRepositoryDialog> {
	return {
		disabled: false,
		error: null,
		onBack: vi.fn(),
		onChange: vi.fn(),
		onClose: vi.fn(),
		onContinue: vi.fn(),
		onError: vi.fn(),
		open: true,
		value: { remoteUrl: "https://github.com/acme/web-app.git", destinationParent: "/code" },
		...overrides,
	};
}

function renderCloneDialog(
	overrides: Partial<React.ComponentProps<typeof CloneRepositoryDialog>> = {},
) {
	const props = cloneDialogProps(overrides);
	return { props, view: render(React.createElement(CloneRepositoryDialog, props)) };
}

afterEach(() => {
	window.localStorage.removeItem("ao.clone.lastDestinationParent");
	vi.restoreAllMocks();
});

describe("clone repository input", () => {
	it("edits the destination without opening the picker and submits the updated path", async () => {
		window.ao!.app.checkGitRepository = vi.fn().mockResolvedValue(true);
		window.ao!.app.chooseDirectory = vi.fn();
		const { props, view } = renderCloneDialog();
		const input = screen.getByRole("textbox", { name: "Destination folder" });
		fireEvent.click(input);
		fireEvent.change(input, { target: { value: "/Projects/new folder" } });
		expect(window.ao!.app.chooseDirectory).not.toHaveBeenCalled();
		expect(props.onChange).toHaveBeenCalledWith({ ...props.value, destinationParent: "/Projects/new folder" });
		view.rerender(React.createElement(CloneRepositoryDialog, { ...props, value: { ...props.value, destinationParent: "/Projects/new folder" } }));
		expect(screen.getByText("Repository will be created at /Projects/new folder/web-app.")).toBeInTheDocument();
		await waitFor(() => expect(screen.getByRole("button", { name: "Continue" })).toBeEnabled());
		fireEvent.click(screen.getByRole("button", { name: "Continue" }));
		expect(props.onContinue).toHaveBeenCalledWith({ ...props.value, destinationParent: "/Projects/new folder", targetPath: "/Projects/new folder/web-app" });
		expect(window.localStorage.getItem("ao.clone.lastDestinationParent")).toBe("/Projects/new folder");
	});

	it("opens the destination picker at the default folder and submits its selection", async () => {
		window.ao!.app.checkGitRepository = vi.fn().mockResolvedValue(true);
		window.ao!.app.chooseDirectory = vi.fn().mockResolvedValue("/Projects/new folder");
		const { props, view } = renderCloneDialog();
		fireEvent.click(screen.getByRole("button", { name: "Choose where to clone the repository" }));
		expect(window.ao!.app.chooseDirectory).toHaveBeenCalledWith({
			title: "Choose where to clone the repository", defaultPath: "~/ao/projects",
		});
		await waitFor(() => expect(props.onChange).toHaveBeenCalledWith({ ...props.value, destinationParent: "/Projects/new folder" }));
		view.rerender(React.createElement(CloneRepositoryDialog, { ...props, value: { ...props.value, destinationParent: "/Projects/new folder" } }));
		expect(screen.getByText("Repository will be created at /Projects/new folder/web-app.")).toBeInTheDocument();
		await waitFor(() => expect(screen.getByRole("button", { name: "Continue" })).toBeEnabled());
		fireEvent.click(screen.getByRole("button", { name: "Continue" }));
		expect(props.onContinue).toHaveBeenCalledWith({ ...props.value, destinationParent: "/Projects/new folder", targetPath: "/Projects/new folder/web-app" });
		expect(window.localStorage.getItem("ao.clone.lastDestinationParent")).toBe("/Projects/new folder");
	});

	it("describes the destination consistently and shows the exact checkout path", () => {
		renderCloneDialog();

		expect(screen.getByText("Destination folder")).toBeInTheDocument();
		expect(screen.getByText("Repository will be created at /code/web-app.")).toBeInTheDocument();
		expect(screen.queryByText(/parent folder/i)).not.toBeInTheDocument();
	});

	it.each([
		["https://github.com/acme/web-app.git", "web-app"],
		["https://git.example.com/web-app.git", "web-app"],
		["ssh://git@github.com/acme/web-app.git", "web-app"],
		["git@github.com:acme/web-app.git", "web-app"],
		["file:///tmp/web-app", "web-app"],
		["file:///tmp/my%20repo.git", "my repo"],
		["https://github.com/acme/nested%2Frepo.git", "repo"],
		["file:///tmp/literal%252Frepo.git", "literal%2Frepo"],
	])("derives the checkout name from %s", (remoteUrl, expected) => {
		expect(repositoryNameFromGitUrl(remoteUrl)).toBe(expected);
	});

	it.each([
		"repository-without-a-scheme",
		"--upload-pack=malicious",
		"https://user:secret@example.com/acme/repo.git",
		"https://example.com/acme/repo.git?access_token=secret",
		"ssh://git:secret@example.com/acme/repo.git",
		"https://github.com/acme/two words.git",
		"https://github.com/acme/web-app/pull/123",
		"https://github.com/acme/web-app/issues/12",
		"https://gitlab.com/acme/web-app/-/merge_requests/12",
		"file:///tmp/bad%ZZ.git",
	])("rejects unsafe or incomplete URL %s", (remoteUrl) => {
		expect(repositoryNameFromGitUrl(remoteUrl)).toBeNull();
	});

	it("joins POSIX and Windows destinations", () => {
		expect(joinCloneDestination("/Users/me/Code/", "web-app")).toBe("/Users/me/Code/web-app");
		expect(joinCloneDestination("C:\\Code\\", "web-app")).toBe("C:\\Code\\web-app");
	});

	it("compares paths with the platform's case rules", () => {
		expect(sameProjectPath("/code/Web-App", "/code/web-app", false)).toBe(false);
		expect(sameProjectPath("C:\\Code\\Web-App", "C:\\Code\\web-app\\", true)).toBe(true);
	});

	it("compares paths case-insensitively by default on macOS", () => {
		vi.spyOn(window.navigator, "platform", "get").mockReturnValue("MacIntel");
		expect(sameProjectPath("/Users/me/Code/Web-App", "/Users/me/Code/web-app")).toBe(true);
	});

	it("allows the same project name at a different target path", async () => {
		window.ao!.app.checkGitRepository = vi.fn().mockResolvedValue(true);
		renderCloneDialog({
			existingProjectNames: ["web-app"],
			existingProjectPaths: ["/other/web-app"],
		});

		await waitFor(() => expect(screen.getByRole("button", { name: "Continue" })).toBeEnabled());
		expect(screen.queryByText("A project with this name already exists.")).not.toBeInTheDocument();
	});

	it("reports a duplicate target once and does not repeat it as a parent alert", async () => {
		window.ao!.app.checkGitRepository = vi.fn().mockResolvedValue(true);
		const onError = vi.fn();
		const { props, view } = renderCloneDialog({
			existingProjectPaths: ["/code/web-app/"],
			onError,
		});

		const duplicateMessage = "A project already exists at this location";
		expect(onError).not.toHaveBeenCalled();
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();
		fireEvent.blur(screen.getByRole("textbox", { name: "Repository URL" }));
		await waitFor(() => expect(onError).toHaveBeenCalledWith(duplicateMessage));
		expect(onError).toHaveBeenCalledTimes(1);
		expect(screen.getAllByRole("alert")).toHaveLength(1);
		await waitFor(() => expect(screen.getByRole("dialog", { name: "Clone a Git repository" })).toHaveClass("modal-shake"));

		view.rerender(React.createElement(CloneRepositoryDialog, { ...props, error: duplicateMessage }));
		expect(screen.getAllByRole("alert")).toHaveLength(1);
		expect(onError).toHaveBeenCalledTimes(1);
	});

	it("waits until blur to report a malformed URL, then reports it once", async () => {
		const onError = vi.fn();
		const { props, view } = renderCloneDialog({
			onError,
			value: { remoteUrl: "", destinationParent: "/code" },
		});
		view.rerender(React.createElement(CloneRepositoryDialog, {
			...props,
			value: { remoteUrl: "n", destinationParent: "/code" },
		}));

		expect(onError).not.toHaveBeenCalled();
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();
		fireEvent.blur(screen.getByRole("textbox", { name: "Repository URL" }));
		await waitFor(() => expect(onError).toHaveBeenCalledWith("Enter a valid HTTPS, SSH, Git, or file URL."));
		expect(onError).toHaveBeenCalledTimes(1);
		expect(screen.getAllByRole("alert")).toHaveLength(1);
		await waitFor(() => expect(screen.getByRole("dialog", { name: "Clone a Git repository" })).toHaveClass("modal-shake"));
	});

	it("reports a malformed URL when the form is submitted before blur", async () => {
		const onError = vi.fn();
		renderCloneDialog({
			onError,
			value: { remoteUrl: "not-a-repository", destinationParent: "/code" },
		});

		const input = screen.getByRole("textbox", { name: "Repository URL" });
		fireEvent.submit(input.closest("form")!);
		await waitFor(() => expect(onError).toHaveBeenCalledWith("Enter a valid HTTPS, SSH, Git, or file URL."));
	});

	it("keeps Continue disabled until a destination is selected", async () => {
		window.ao!.app.checkGitRepository = vi.fn().mockResolvedValue(true);
		const onContinue = vi.fn();
		const onError = vi.fn();
		renderCloneDialog({
			onContinue,
			onError,
			value: { remoteUrl: "https://github.com/acme/web-app.git", destinationParent: "" },
		});

		await waitFor(() => expect(window.ao!.app.checkGitRepository).toHaveBeenCalledOnce());
		const continueButton = screen.getByRole("button", { name: "Continue" });
		expect(continueButton).toBeDisabled();
		fireEvent.submit(continueButton.closest("form")!);
		expect(onContinue).not.toHaveBeenCalled();
		expect(onError).toHaveBeenCalledWith("Choose a destination folder.");
	});

	it("ignores an older remote check after the URL changes", async () => {
		let resolveFirst: ((exists: boolean) => void) | undefined;
		let resolveSecond: ((exists: boolean) => void) | undefined;
		window.ao!.app.checkGitRepository = vi.fn((url: string) => new Promise<boolean>((resolve) => {
			if (url.includes("first")) resolveFirst = resolve;
			else resolveSecond = resolve;
		}));
		const onError = vi.fn();
		const { props: firstProps, view } = renderCloneDialog({
			onError,
			value: { remoteUrl: "https://git.example.com/first.git", destinationParent: "/code" },
		});

		await waitFor(() => expect(window.ao!.app.checkGitRepository).toHaveBeenCalledWith("https://git.example.com/first.git"));
		view.rerender(React.createElement(CloneRepositoryDialog, {
			...firstProps,
			value: { remoteUrl: "https://git.example.com/second.git", destinationParent: "/code" },
		}));
		await waitFor(() => expect(window.ao!.app.checkGitRepository).toHaveBeenCalledWith("https://git.example.com/second.git"));

		await act(async () => resolveSecond?.(true));
		await waitFor(() => expect(screen.getByRole("button", { name: "Continue" })).toBeEnabled());
		await act(async () => resolveFirst?.(false));

		expect(screen.getByRole("button", { name: "Continue" })).toBeEnabled();
		expect(onError).not.toHaveBeenCalled();
	});

	it("checks the remote again each time the dialog opens", async () => {
		window.ao!.app.checkGitRepository = vi.fn().mockResolvedValue(true);
		const { props, view } = renderCloneDialog();

		await waitFor(() => expect(screen.getByRole("button", { name: "Continue" })).toBeEnabled());
		view.rerender(React.createElement(CloneRepositoryDialog, { ...props, open: false }));
		view.rerender(React.createElement(CloneRepositoryDialog, { ...props, open: true }));

		expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled();
		await waitFor(() => expect(window.ao!.app.checkGitRepository).toHaveBeenCalledTimes(2));
		await waitFor(() => expect(screen.getByRole("button", { name: "Continue" })).toBeEnabled());
	});

	it("announces an unavailable repository and shakes the dialog", async () => {
		window.ao!.app.checkGitRepository = vi.fn().mockResolvedValue(false);
		const onError = vi.fn();
		renderCloneDialog({ onError });

		await waitFor(() => expect(onError).toHaveBeenCalledWith("This isn't a repository or you don't have access"));
		const input = screen.getByRole("textbox", { name: "Repository URL" });
		expect(input).toHaveAttribute("aria-invalid", "true");
		expect(input).toHaveAttribute("aria-describedby", expect.stringContaining("cloneRepositoryUrlError"));
		expect(screen.getByRole("alert")).toHaveTextContent("This isn't a repository or you don't have access");
		await waitFor(() => expect(screen.getByRole("dialog", { name: "Clone a Git repository" })).toHaveClass("modal-shake"));
	});

	it("discards a destination picker result after the dialog closes", async () => {
		let resolvePicker: ((path: string) => void) | undefined;
		window.ao!.app.chooseDirectory = vi.fn(() => new Promise<string>((resolve) => {
			resolvePicker = resolve;
		}));
		const onChange = vi.fn();
		const { props, view } = renderCloneDialog({ onChange });

		fireEvent.click(screen.getByRole("button", { name: "Choose where to clone the repository" }));
		await waitFor(() => expect(window.ao!.app.chooseDirectory).toHaveBeenCalledOnce());
		view.rerender(React.createElement(CloneRepositoryDialog, { ...props, open: false }));
		await act(async () => resolvePicker?.("/stale/path"));

		expect(onChange).not.toHaveBeenCalled();
	});
});
