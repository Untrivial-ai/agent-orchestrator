import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { SessionInterfaceTransition } from "../hooks/useSessionInterfaceTransition";
import {
	SessionInterfaceActionGroup,
	SessionInterfaceSwitchButton,
	SessionInterfaceSwitchDialog,
	SessionInterfaceTransitionNotice,
} from "./SessionInterfaceSwitch";
import { TooltipProvider } from "./ui/tooltip";

function renderSwitchButton(target: "chat" | "tui", onClick = vi.fn()) {
	return {
		onClick,
		...render(
			<TooltipProvider>
				<SessionInterfaceSwitchButton target={target} supported onClick={onClick} />
			</TooltipProvider>,
		),
	};
}

function transition(phase: SessionInterfaceTransition["phase"]): SessionInterfaceTransition {
	return {
		id: "switch-1",
		sessionId: "project-1",
		sourceMode: "tui",
		targetMode: "chat",
		policy: "drain",
		historyPolicy: "strict",
		phase,
		createdAt: "2026-08-05T10:00:00Z",
		updatedAt: "2026-08-05T10:00:01Z",
	};
}

describe("SessionInterfaceSwitchButton", () => {
	it("uses the shared topbar spacing between adjacent session actions", () => {
		render(
			<SessionInterfaceActionGroup>
				<button type="button">First action</button>
				<button type="button">Second action</button>
			</SessionInterfaceActionGroup>,
		);

		const group = screen.getByRole("button", { name: "First action" }).parentElement;
		expect(group).toHaveClass("gap-2");
		expect(group).not.toHaveClass("gap-px");
	});

	it("shows a spinner while switching and reveals cancel on the tab hover target", () => {
		const onCancel = vi.fn();
		render(
			<TooltipProvider>
				{/* TerminalTabFrame marks the session tab with Tailwind `group`. */}
				<div className="group">
					<SessionInterfaceSwitchButton
						target="chat"
						supported
						transition={transition("draining")}
						onClick={vi.fn()}
						onCancel={onCancel}
					/>
				</div>
			</TooltipProvider>,
		);

		const status = screen.getByRole("status");
		expect(status).toHaveAttribute(
			"aria-label",
			"Waiting to switch… Switching to Chat UI.",
		);
		expect(status.querySelector(".animate-spin")).not.toBeNull();
		const cancel = screen.getByRole("button", { name: "Cancel switch to Chat UI" });
		expect(cancel).toHaveClass("opacity-0", "group-hover:opacity-100");
		fireEvent.click(cancel);
		expect(onCancel).toHaveBeenCalledOnce();
	});

	it.each([
		["source_stopping", "Stopping controller… Switching to Chat UI."],
		["target_starting", "Resuming agent… Switching to Chat UI."],
	] as const)("stays non-interactive while %s is progressing", (phase, expectedLabel) => {
		render(
			<TooltipProvider>
				<SessionInterfaceSwitchButton
					target="chat"
					supported
					transition={transition(phase)}
					onClick={vi.fn()}
					onCancel={vi.fn()}
				/>
			</TooltipProvider>,
		);

		const status = screen.getByRole("status");
		expect(status).toHaveAttribute("aria-label", expectedLabel);
		expect(status.querySelector(".animate-spin")).not.toBeNull();
		expect(screen.queryByRole("button", { name: "Cancel switch to Chat UI" })).not.toBeInTheDocument();
	});

	it("replaces progress with a non-interactive warning when target shutdown is unconfirmed", () => {
		const detail =
			"AO could not confirm the target controller stopped. Restart AO to retry shutdown before restoring the original interface.";
		render(
			<TooltipProvider>
				<SessionInterfaceSwitchButton
					target="chat"
					supported
					transition={{
						...transition("target_starting"),
						errorCode: "TARGET_STOP_UNCONFIRMED",
						errorDetail: detail,
					}}
					onClick={vi.fn()}
					onCancel={vi.fn()}
				/>
			</TooltipProvider>,
		);

		const status = screen.getByRole("status");
		expect(status).toHaveAttribute("aria-label", `Interface switch needs attention. ${detail}`);
		expect(status.querySelector(".animate-spin")).toBeNull();
		expect(screen.queryByRole("button")).not.toBeInTheDocument();
	});

	it.each([
		["chat", "Switch to chat UI", "lucide-message-square"],
		["tui", "Switch to terminal UI", "lucide-square-terminal"],
	] as const)("uses an icon-only %s destination control", (target, label, iconClass) => {
		const { onClick } = renderSwitchButton(target);

		const button = screen.getByRole("button", { name: label });
		expect(button.classList.contains("topbar-control--icon")).toBe(true);
		expect(button.textContent).toBe("");
		expect(button.querySelector(`.${iconClass}`)).not.toBeNull();
		fireEvent.click(button);
		expect(onClick).toHaveBeenCalledOnce();
	});

	it("describes recovered history accurately and hides it after durable acknowledgement", () => {
		const recovered = { ...transition("recovery_required"), errorCode: "DAEMON_RESTARTED" };
		const { rerender } = render(
			<SessionInterfaceTransitionNotice transition={recovered} onDismiss={vi.fn()} />,
		);

		expect(screen.getByText("Interface switch recovered")).toBeInTheDocument();
		expect(screen.queryByText("Interface switch needs recovery")).not.toBeInTheDocument();

		rerender(
			<SessionInterfaceTransitionNotice
				transition={{ ...recovered, noticeAcknowledgedAt: "2026-08-13T08:00:00Z" }}
				onDismiss={vi.fn()}
			/>,
		);
		expect(screen.queryByText("Interface switch recovered")).not.toBeInTheDocument();
	});

	it("does not claim an unresolved recovery-required transition was recovered", () => {
		render(
			<SessionInterfaceTransitionNotice
				transition={{
					...transition("recovery_required"),
					errorCode: "SOURCE_STOP_UNCERTAIN",
				}}
				onDismiss={vi.fn()}
			/>,
		);

		expect(screen.getByText("Interface switch needs attention")).toBeInTheDocument();
		expect(screen.queryByText("Interface switch recovered")).not.toBeInTheDocument();
	});
});

describe("SessionInterfaceSwitchDialog", () => {
	it("focuses Finish work as the safe default", async () => {
		render(<SessionInterfaceSwitchDialog open target="tui" onOpenChange={vi.fn()} onChoose={vi.fn()} />);

		await waitFor(() => {
			expect(screen.getByRole("button", { name: /^Finish work, then switch/ })).toHaveFocus();
		});
	});

	it("names active, queued, draft, and staged-attachment consequences before a Chat escape", () => {
		render(<SessionInterfaceSwitchDialog open target="tui" onOpenChange={vi.fn()} onChoose={vi.fn()} />);

		const dialog = screen.getByRole("dialog", { name: "Switch to Terminal UI?" });
		expect(within(dialog).getByRole("button", { name: /^Finish work, then switch/ })).toHaveTextContent(
			"running turn and anything already queued to finish",
		);
		expect(within(dialog).getByRole("button", { name: /^Stop now and switch/ })).toHaveTextContent(
			"unfinished output and queued Chat turns are cancelled",
		);
		expect(dialog).toHaveTextContent(
			"Any unsent Chat draft or staged attachments are discarded when the switch completes.",
		);
	});

	it("discloses that interrupting discards an unsent terminal draft", () => {
		render(<SessionInterfaceSwitchDialog open target="chat" onOpenChange={vi.fn()} onChoose={vi.fn()} />);

		expect(screen.getByText(/Any unsent Terminal UI draft is discarded/)).toBeInTheDocument();
	});
});

describe("SessionInterfaceTransitionNotice", () => {
	it.each([undefined, "2026-08-13T08:00:00Z"])(
		"keeps unconfirmed target shutdown visible without unsafe recovery or dismissal actions (%s)",
		(noticeAcknowledgedAt) => {
			const detail =
				"AO could not confirm the target controller stopped. Restart AO to retry shutdown before restoring the original interface. target still running";
			render(
				<SessionInterfaceTransitionNotice
					transition={{
						...transition("target_starting"),
						errorCode: "TARGET_STOP_UNCONFIRMED",
						errorDetail: detail,
						noticeAcknowledgedAt,
					}}
					onDismiss={vi.fn()}
					dismissing
					onRetry={vi.fn()}
					retrying
					onUseProviderHistory={vi.fn()}
					onSwitchWithInterrupt={vi.fn()}
				/>,
			);

			const alert = screen.getByRole("alert");
			expect(alert).toHaveTextContent("Interface switch needs attention");
			expect(alert).toHaveTextContent(detail);
			expect(alert.querySelector(".animate-spin")).toBeNull();
			expect(screen.queryByRole("button")).not.toBeInTheDocument();
		},
	);

	it("provides the restart instruction if unconfirmed target shutdown has no detail", () => {
		render(
			<SessionInterfaceTransitionNotice
				transition={{ ...transition("target_starting"), errorCode: "TARGET_STOP_UNCONFIRMED" }}
				onDismiss={vi.fn()}
			/>,
		);

		expect(screen.getByRole("alert")).toHaveTextContent(
			"AO could not confirm the target controller stopped. Restart AO to retry shutdown before restoring the original interface.",
		);
		expect(screen.queryByText(/original interface remains available/)).not.toBeInTheDocument();
	});

	it("announces unsettled history and offers retry or stay actions", () => {
		const onRetry = vi.fn();
		const onDismiss = vi.fn();
		render(
			<SessionInterfaceTransitionNotice
				transition={{
					...transition("failed"),
					errorCode: "TARGET_HISTORY_UNSETTLED",
					errorDetail: "Interface switch failed (AO-2L): target history is not settled.",
				}}
				onDismiss={onDismiss}
				onRetry={onRetry}
			/>,
		);

		expect(screen.getByRole("alert")).toHaveTextContent("AO-2L");
		fireEvent.click(screen.getByRole("button", { name: "Retry switch to Chat UI" }));
		expect(onRetry).toHaveBeenCalledOnce();
		expect(screen.queryByRole("button", { name: "Use provider history and switch" })).not.toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "Stay in Terminal" }));
		expect(onDismiss).toHaveBeenCalledOnce();
	});

	it("offers provider-history recovery only for a legacy text mismatch", () => {
		const onRetry = vi.fn();
		const onUseProviderHistory = vi.fn();
		const onDismiss = vi.fn();
		render(
			<SessionInterfaceTransitionNotice
				transition={{
					...transition("failed"),
					errorCode: "TARGET_HISTORY_UNTRUSTED_TEXT_MISMATCH",
					errorDetail: "Interface switch failed (AO-2L): legacy checkpoint text did not match.",
				}}
				onDismiss={onDismiss}
				onRetry={onRetry}
				onUseProviderHistory={onUseProviderHistory}
			/>,
		);

		expect(screen.getByRole("alert")).toHaveTextContent("AO-2L");
		fireEvent.click(screen.getByRole("button", { name: "Retry switch to Chat UI" }));
		fireEvent.click(screen.getByRole("button", { name: "Use provider history and switch" }));
		fireEvent.click(screen.getByRole("button", { name: "Stay in Terminal" }));
		expect(onRetry).toHaveBeenCalledOnce();
		expect(onUseProviderHistory).toHaveBeenCalledOnce();
		expect(onDismiss).toHaveBeenCalledOnce();
	});

	it("retains explicit provider-history recovery after daemon restart", () => {
		const onRetry = vi.fn();
		const onUseProviderHistory = vi.fn();
		const onDismiss = vi.fn();
		render(
			<SessionInterfaceTransitionNotice
				transition={{
					...transition("recovery_required"),
					historyPolicy: "provider_history",
					errorCode: "DAEMON_RESTARTED",
					errorDetail: "AO restored Terminal after the daemon restarted.",
				}}
				onDismiss={onDismiss}
				onRetry={onRetry}
				onUseProviderHistory={onUseProviderHistory}
			/>,
		);

		expect(screen.getByRole("status")).toHaveTextContent("AO restored Terminal");
		fireEvent.click(screen.getByRole("button", { name: "Retry switch to Chat UI" }));
		fireEvent.click(screen.getByRole("button", { name: "Use provider history and switch" }));
		fireEvent.click(screen.getByRole("button", { name: "Stay in Terminal" }));
		expect(onRetry).toHaveBeenCalledOnce();
		expect(onUseProviderHistory).toHaveBeenCalledOnce();
		expect(onDismiss).toHaveBeenCalledOnce();
	});

	it("announces a rejected recovery attempt once inside AO-2L", () => {
		render(
			<SessionInterfaceTransitionNotice
				transition={{
					...transition("failed"),
					errorCode: "TARGET_HISTORY_UNTRUSTED_TEXT_MISMATCH",
					errorDetail: "Interface switch failed (AO-2L).",
				}}
				onDismiss={vi.fn()}
				onRetry={vi.fn()}
				recoveryError="Terminal history changed; retry with a fresh choice."
			/>,
		);

		const [announcement] = screen.getAllByRole("alert");
		expect(screen.getAllByRole("alert")).toHaveLength(1);
		expect(announcement).toHaveTextContent("Interface switch failed (AO-2L).");
		expect(announcement).toHaveTextContent(
			"Recovery attempt failed: Terminal history changed; retry with a fresh choice.",
		);
	});

	it("announces a notice dismissal failure once inside AO-2L", () => {
		render(
			<SessionInterfaceTransitionNotice
				transition={{
					...transition("failed"),
					errorCode: "TARGET_HISTORY_UNSETTLED",
					errorDetail: "Interface switch failed (AO-2L).",
				}}
				onDismiss={vi.fn()}
				dismissError="Dismiss request was rejected."
			/>,
		);

		const [announcement] = screen.getAllByRole("alert");
		expect(screen.getAllByRole("alert")).toHaveLength(1);
		expect(announcement).toHaveTextContent("Interface switch failed (AO-2L).");
		expect(announcement).toHaveTextContent("Could not dismiss this message. Try again.");
	});

	it("offers an explicit discard action when drain preserves a draft", () => {
		const onSwitchWithInterrupt = vi.fn();
		render(
			<SessionInterfaceTransitionNotice
				transition={{
					...transition("failed"),
					errorCode: "DRAIN_DRAFT_PRESENT",
					errorDetail: "AO found unsent text and left the source untouched.",
				}}
				onDismiss={vi.fn()}
				onSwitchWithInterrupt={onSwitchWithInterrupt}
			/>,
		);

		const action = screen.getByRole("button", {
			name: "Discard draft and switch",
		});
		fireEvent.click(action);
		expect(onSwitchWithInterrupt).toHaveBeenCalledOnce();
	});

	it("offers an explicit cancellation action when a provider decision blocks drain", () => {
		const onSwitchWithInterrupt = vi.fn();
		render(
			<SessionInterfaceTransitionNotice
				transition={{
					...transition("failed"),
					errorCode: "DRAIN_DECISION_PENDING",
					errorDetail: "AO found a provider decision waiting in Terminal.",
				}}
				onDismiss={vi.fn()}
				onSwitchWithInterrupt={onSwitchWithInterrupt}
			/>,
		);

		const action = screen.getByRole("button", {
			name: "Cancel request and switch",
		});
		fireEvent.click(action);
		expect(onSwitchWithInterrupt).toHaveBeenCalledOnce();
	});

	it("does not offer a destructive retry for unrelated failures", () => {
		render(
			<SessionInterfaceTransitionNotice
				transition={{
					...transition("failed"),
					errorCode: "TARGET_UNAVAILABLE",
				}}
				onDismiss={vi.fn()}
				onSwitchWithInterrupt={vi.fn()}
			/>,
		);

		expect(screen.queryByRole("button", { name: "Stop now and switch" })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Discard draft and switch" })).not.toBeInTheDocument();
	});

	it("does not offer a destructive retry when terminal quiescence is unverified", () => {
		render(
			<SessionInterfaceTransitionNotice
				transition={{
					...transition("failed"),
					errorCode: "DRAIN_QUIESCENCE_UNVERIFIED",
					errorDetail: "AO could not classify the current Terminal screen.",
				}}
				onDismiss={vi.fn()}
				onSwitchWithInterrupt={vi.fn()}
			/>,
		);

		expect(screen.queryByRole("button", { name: "Stop now and switch" })).not.toBeInTheDocument();
	});
});
