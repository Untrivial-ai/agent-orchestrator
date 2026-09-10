import * as AccordionPrimitive from "@radix-ui/react-accordion";
import type { ComponentPropsWithoutRef, ReactNode } from "react";
import { cn } from "../../lib/utils";

export const Accordion = AccordionPrimitive.Root;
export const AccordionItem = AccordionPrimitive.Item;

export function AccordionTrigger({
	className,
	headerClassName,
	trailing,
	...props
}: ComponentPropsWithoutRef<typeof AccordionPrimitive.Trigger> & {
	/** Classes for the shared row wrapper (Radix's <h3> Header), which Radix
	 *  stamps with data-state=open|closed. */
	headerClassName?: string;
	/** Rendered as a sibling of the trigger <button>, inside the same Header —
	 *  lets callers place another interactive element (e.g. a copy button)
	 *  next to the trigger without nesting a <button> inside a <button>. */
	trailing?: ReactNode;
}) {
	return (
		<AccordionPrimitive.Header
			className={cn(
				"group/row flex items-center rounded-lg transition-colors hover:bg-interactive-hover data-[state=open]:bg-interactive-active",
				headerClassName,
			)}
		>
			<AccordionPrimitive.Trigger
				className={cn(
					"flex min-w-0 flex-1 items-center text-left focus-visible:outline-none",
					className,
				)}
				{...props}
			/>
			{trailing}
		</AccordionPrimitive.Header>
	);
}

export function AccordionContent({
	className,
	...props
}: ComponentPropsWithoutRef<typeof AccordionPrimitive.Content>) {
	return <AccordionPrimitive.Content className={cn("overflow-hidden", className)} {...props} />;
}
