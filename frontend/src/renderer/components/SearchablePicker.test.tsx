import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { expect, it } from "vitest";
import { SearchablePicker } from "./SearchablePicker";

it("filters a fixed repository list and selects the matching private repository", async () => {
	const user = userEvent.setup();
	function Example() {
		const [value, setValue] = useState("");
		return <SearchablePicker
			ariaLabel="Repository"
			placeholder="Select a repository"
			searchPlaceholder="Search repositories"
			value={value}
			onChange={setValue}
			options={[
				{ value: "one", label: "acme/public" },
				{ value: "two", label: "acme/private", private: true },
			]}
		/>;
	}
	render(<Example />);

	await user.click(screen.getByRole("combobox", { name: "Repository" }));
	await user.type(screen.getByPlaceholderText("Search repositories"), "private");
	expect(screen.queryByRole("option", { name: "acme/public" })).not.toBeInTheDocument();
	const option = screen.getByRole("option", { name: "acme/private" });
	expect(option.querySelector("svg.lucide-lock")).not.toBeNull();
	await user.click(option);
	expect(screen.getByRole("combobox", { name: "Repository" })).toHaveTextContent("acme/private");
});

it("keeps a large result list scrollable below the search field without empty space for short lists", async () => {
	const user = userEvent.setup();
	render(<SearchablePicker
		ariaLabel="Template"
		placeholder="Default"
		searchPlaceholder="Search templates"
		value=""
		onChange={() => undefined}
		options={Array.from({ length: 40 }, (_, index) => ({ value: String(index), label: `Template ${index}` }))}
	/>);
	await user.click(screen.getByRole("combobox", { name: "Template" }));
	const list = screen.getByRole("listbox", { name: "Template" });
	expect(list).toHaveClass("overflow-y-auto");
	expect(list).toHaveClass("max-h-72");
	expect(screen.getByPlaceholderText("Search templates")).toBeInTheDocument();
	expect(screen.getAllByRole("option")).toHaveLength(40);
});

it("gives repository selectors a fixed, visibly scrollable result area", async () => {
	const user = userEvent.setup();
	render(<SearchablePicker
		ariaLabel="Repository"
		placeholder="Select a repository"
		searchPlaceholder="Search repositories"
		fixedScroll
		value=""
		onChange={() => undefined}
		options={Array.from({ length: 40 }, (_, index) => ({ value: String(index), label: `repo-${index}` }))}
	/>);
	await user.click(screen.getByRole("combobox", { name: "Repository" }));
	const list = screen.getByRole("listbox", { name: "Repository" });
	expect(list).toHaveClass("h-72", "overflow-y-scroll", "repository-picker-scrollbar");
	expect(screen.getAllByRole("option")).toHaveLength(40);
});

// The popover is portaled to <body>, so inside a modal Dialog react-remove-scroll
// preventDefault()s the native wheel scroll. The picker drives the scroll itself
// from a non-passive wheel listener, which must survive that preventDefault.
it("scrolls an overflowing result list from the wheel and cancels the native scroll", async () => {
	const user = userEvent.setup();
	render(<SearchablePicker
		ariaLabel="Repository"
		placeholder="Select a repository"
		searchPlaceholder="Search repositories"
		fixedScroll
		value=""
		onChange={() => undefined}
		options={Array.from({ length: 40 }, (_, index) => ({ value: String(index), label: `repo-${index}` }))}
	/>);
	await user.click(screen.getByRole("combobox", { name: "Repository" }));
	const list = screen.getByRole("listbox", { name: "Repository" });

	// jsdom does no layout, so drive scrollTop/overflow through explicit descriptors.
	let scrollTop = 0;
	Object.defineProperty(list, "scrollTop", { configurable: true, get: () => scrollTop, set: (value) => { scrollTop = value; } });
	Object.defineProperty(list, "scrollHeight", { configurable: true, value: 1000 });
	Object.defineProperty(list, "clientHeight", { configurable: true, value: 288 });

	const wheel = new WheelEvent("wheel", { deltaY: 120, bubbles: true, cancelable: true });
	list.dispatchEvent(wheel);
	expect(scrollTop).toBe(120);
	expect(wheel.defaultPrevented).toBe(true);
});

it("leaves a non-overflowing result list to scroll natively", async () => {
	const user = userEvent.setup();
	render(<SearchablePicker
		ariaLabel="Repository"
		placeholder="Select a repository"
		searchPlaceholder="Search repositories"
		fixedScroll
		value=""
		onChange={() => undefined}
		options={[{ value: "one", label: "repo-one" }]}
	/>);
	await user.click(screen.getByRole("combobox", { name: "Repository" }));
	const list = screen.getByRole("listbox", { name: "Repository" });

	let scrollTop = 0;
	Object.defineProperty(list, "scrollTop", { configurable: true, get: () => scrollTop, set: (value) => { scrollTop = value; } });
	Object.defineProperty(list, "scrollHeight", { configurable: true, value: 100 });
	Object.defineProperty(list, "clientHeight", { configurable: true, value: 288 });

	const wheel = new WheelEvent("wheel", { deltaY: 120, bubbles: true, cancelable: true });
	list.dispatchEvent(wheel);
	expect(scrollTop).toBe(0);
	expect(wheel.defaultPrevented).toBe(false);
});
