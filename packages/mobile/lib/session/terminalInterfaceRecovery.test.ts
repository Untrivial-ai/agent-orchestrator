import { beforeEach, describe, expect, it, vi } from "vitest";

const alert = vi.hoisted(() => vi.fn());
vi.mock("react-native", () => ({ Alert: { alert } }));

import { terminalInterfaceFailureRecovery } from "./terminalInterfaceRecovery";

describe("terminalInterfaceFailureRecovery", () => {
	beforeEach(() => alert.mockReset());

	it("offers an explicit destructive interrupt retry when a preserved draft blocks drain", () => {
		const recovery = terminalInterfaceFailureRecovery({ errorCode: "DRAIN_DRAFT_PRESENT" });

		expect(recovery).toMatchObject({
			actionLabel: "Discard draft and switch",
			policy: "interrupt",
			confirmStyle: "destructive",
		});
		expect(recovery?.confirmationTitle).toMatch(/discard draft/i);
		expect(recovery?.confirmationMessage).toMatch(/unsent terminal draft/i);
		expect(recovery?.confirmationMessage).toMatch(/cannot be undone/i);
	});

	it("keeps the draft on cancel and invokes only the explicit interrupt confirmation", () => {
		const recovery = terminalInterfaceFailureRecovery({ errorCode: "DRAIN_DRAFT_PRESENT" });
		const start = vi.fn();
		recovery?.confirm(start);

		expect(alert).toHaveBeenCalledOnce();
		const buttons = alert.mock.calls[0]?.[2];
		expect(buttons?.[0]).toMatchObject({ text: "Keep draft", style: "cancel" });
		buttons?.[0]?.onPress?.();
		expect(start).not.toHaveBeenCalled();

		expect(buttons?.[1]).toMatchObject({ text: "Discard draft and switch", style: "destructive" });
		buttons?.[1]?.onPress?.();
		expect(start).toHaveBeenCalledOnce();
		expect(start).toHaveBeenCalledWith("interrupt");
	});

	it("offers an explicit destructive cancellation when a provider decision blocks drain", () => {
		const recovery = terminalInterfaceFailureRecovery({ errorCode: "DRAIN_DECISION_PENDING" });

		expect(recovery).toMatchObject({
			actionLabel: "Cancel request and switch",
			policy: "interrupt",
			confirmStyle: "destructive",
			confirmationAction: "Cancel request and switch",
		});
		expect(recovery?.confirmationTitle).toMatch(/cancel request/i);
		expect(recovery?.confirmationMessage).toMatch(/pending provider request/i);

		const start = vi.fn();
		recovery?.confirm(start);
		const buttons = alert.mock.calls[0]?.[2];
		expect(buttons?.[0]).toMatchObject({ text: "Keep request", style: "cancel" });
		buttons?.[1]?.onPress?.();
		expect(start).toHaveBeenCalledWith("interrupt");
	});

	it("does not advertise draft destruction for unrelated transition failures", () => {
		expect(terminalInterfaceFailureRecovery({ errorCode: "DRAIN_QUIESCENCE_UNVERIFIED" })).toBeUndefined();
		expect(terminalInterfaceFailureRecovery({ errorCode: "TARGET_RESUME_FAILED" })).toBeUndefined();
		expect(terminalInterfaceFailureRecovery(undefined)).toBeUndefined();
	});
});
