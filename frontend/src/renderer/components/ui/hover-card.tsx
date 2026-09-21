import { HoverCard as HoverCardPrimitive } from "radix-ui";
import { cn } from "../../lib/utils";

function HoverCard({
	openDelay = 300,
	closeDelay = 150,
	...props
}: React.ComponentProps<typeof HoverCardPrimitive.Root>) {
	return (
		<HoverCardPrimitive.Root
			data-slot="hover-card"
			openDelay={openDelay}
			closeDelay={closeDelay}
			{...props}
		/>
	);
}

function HoverCardTrigger({ ...props }: React.ComponentProps<typeof HoverCardPrimitive.Trigger>) {
	return <HoverCardPrimitive.Trigger data-slot="hover-card-trigger" {...props} />;
}

function HoverCardContent({
	className,
	align = "start",
	sideOffset = 6,
	portalContainer,
	...props
}: React.ComponentProps<typeof HoverCardPrimitive.Content> & {
	portalContainer?: React.ComponentProps<typeof HoverCardPrimitive.Portal>["container"];
}) {
	return (
		<HoverCardPrimitive.Portal container={portalContainer}>
			<HoverCardPrimitive.Content
				data-slot="hover-card-content"
				align={align}
				sideOffset={sideOffset}
				className={cn(
					"z-overlay w-72 origin-(--radix-hover-card-content-transform-origin) animate-in rounded-lg border border-border bg-popover text-popover-foreground shadow-md outline-none fade-in-0 zoom-in-95 data-[side=bottom]:slide-in-from-top-2 data-[side=left]:slide-in-from-right-2 data-[side=right]:slide-in-from-left-2 data-[side=top]:slide-in-from-bottom-2 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95",
					className,
				)}
				{...props}
			/>
		</HoverCardPrimitive.Portal>
	);
}

export { HoverCard, HoverCardTrigger, HoverCardContent };
