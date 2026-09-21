import { render } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { keepFocusOnOpenedSurface, useMenuReturnTarget } from "./menu-focus";

const MENU_ID = "menu-content";

// Stands in for what Radix renders while a dropdown is open: a trigger linked
// to the content by `aria-controls`, and the content itself.
function OpenMenu() {
	const contentRef = useMenuReturnTarget<HTMLDivElement>();
	return (
		<>
			<button type="button" aria-controls={MENU_ID} aria-expanded>
				Project actions
			</button>
			<div id={MENU_ID} role="menu" ref={contentRef}>
				<button type="button">New session</button>
			</div>
		</>
	);
}

// A context menu has no `aria-controls` trigger and grabs the caret for itself
// while it opens, which is the case the return target has to survive.
function ContextMenuLike() {
	const contentRef = useMenuReturnTarget<HTMLDivElement>();
	return (
		<div role="menu" ref={contentRef} tabIndex={-1}>
			<button type="button">New session</button>
		</div>
	);
}

async function openContextMenu() {
	const openedFrom = document.createElement("button");
	document.body.append(openedFrom);
	openedFrom.focus();

	const { getByRole } = render(<ContextMenuLike />);
	const menu = getByRole("menu");
	// Radix moves focus into the content as it opens, before any microtask the
	// capture might have deferred to.
	menu.focus();
	await Promise.resolve();
	return { menu, openedFrom };
}

async function openMenu() {
	const { getByRole } = render(<OpenMenu />);
	// The return target is captured a microtask after the content attaches.
	await Promise.resolve();
	return {
		menu: getByRole("menu"),
		item: getByRole("button", { name: "New session" }),
		trigger: getByRole("button", { name: "Project actions" }),
	};
}

/** Radix dispatches its unmount handler on the content element. */
function closeMenu(menu: HTMLElement) {
	const event = new CustomEvent("focusScope.autoFocusOnUnmount", { cancelable: true });
	menu.addEventListener("focusScope.autoFocusOnUnmount", keepFocusOnOpenedSurface as EventListener, {
		once: true,
	});
	menu.dispatchEvent(event);
	return event;
}

function openDialogField() {
	const field = document.createElement("textarea");
	document.body.append(field);
	field.focus();
	return field;
}

/** A Radix dialog closes by unmounting: focus falls through to `document.body`. */
function closeDialogField(field: HTMLElement) {
	field.dispatchEvent(new FocusEvent("focusout", { bubbles: true, relatedTarget: null }));
	field.remove();
}

afterEach(() => {
	vi.useRealTimers();
});

describe("keepFocusOnOpenedSurface", () => {
	it("lets Radix return focus to the trigger when nothing else claimed it", async () => {
		const { menu } = await openMenu();
		expect(closeMenu(menu).defaultPrevented).toBe(false);
	});

	it("lets Radix return focus to the trigger while focus is still in the menu", async () => {
		const { menu, item } = await openMenu();
		item.focus();
		expect(closeMenu(menu).defaultPrevented).toBe(false);
	});

	it("keeps focus where a dialog opened by the selected item put it", async () => {
		const { menu } = await openMenu();
		const field = openDialogField();

		expect(closeMenu(menu).defaultPrevented).toBe(true);
		expect(document.activeElement).toBe(field);
	});

	it("hands focus back to the trigger once that dialog closes", async () => {
		const { menu, trigger } = await openMenu();
		const field = openDialogField();
		closeMenu(menu);

		closeDialogField(field);

		await vi.waitFor(() => expect(document.activeElement).toBe(trigger));
	});

	it("abandons the hand-back once focus finds a home outside the dialog", async () => {
		const { menu, trigger } = await openMenu();
		const field = openDialogField();
		closeMenu(menu);

		// The dialog hands focus straight to another element instead of dropping
		// it, so there is nothing to hand back.
		const elsewhere = document.createElement("input");
		document.body.append(elsewhere);
		field.dispatchEvent(new FocusEvent("focusout", { bubbles: true, relatedTarget: elsewhere }));
		elsewhere.focus();
		field.remove();

		// Much later, an unrelated element loses focus to nowhere. The stale menu
		// trigger must not steal the caret off the back of it.
		const unrelated = document.createElement("button");
		document.body.append(unrelated);
		unrelated.focus();
		unrelated.dispatchEvent(new FocusEvent("focusout", { bubbles: true, relatedTarget: null }));
		unrelated.remove();

		await new Promise((resolve) => setTimeout(resolve, 60));
		expect(document.activeElement).not.toBe(trigger);
	});

	it("keeps waiting while the surface that borrowed the caret is still open", async () => {
		const { menu, trigger } = await openMenu();
		const field = openDialogField();
		closeMenu(menu);

		// A blur elsewhere on the page drops focus while the dialog is still there.
		field.dispatchEvent(new FocusEvent("focusout", { bubbles: true, relatedTarget: null }));
		(document.activeElement as HTMLElement | null)?.blur();
		await new Promise((resolve) => setTimeout(resolve, 60));
		expect(document.activeElement).not.toBe(trigger);

		// Closing it for real still hands the caret back.
		closeDialogField(field);
		await vi.waitFor(() => expect(document.activeElement).toBe(trigger));
	});

	it("returns a context menu to where it was opened from, not to the menu itself", async () => {
		const { menu, openedFrom } = await openContextMenu();
		const field = openDialogField();
		closeMenu(menu);

		closeDialogField(field);

		await vi.waitFor(() => expect(document.activeElement).toBe(openedFrom));
		openedFrom.remove();
	});

	it("leaves focus alone if something else claimed it before the hand-back", async () => {
		const { menu, trigger } = await openMenu();
		const field = openDialogField();
		closeMenu(menu);

		closeDialogField(field);
		const elsewhere = document.createElement("input");
		document.body.append(elsewhere);
		elsewhere.focus();

		await new Promise((resolve) => setTimeout(resolve, 10));
		expect(document.activeElement).toBe(elsewhere);
		expect(document.activeElement).not.toBe(trigger);
	});
});
