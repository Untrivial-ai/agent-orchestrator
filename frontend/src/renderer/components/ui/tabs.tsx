import * as TabsPrimitive from "@radix-ui/react-tabs";
import { cn } from "../../lib/utils";

export const Tabs = TabsPrimitive.Root;

export function TabsList({ className, ...props }: React.ComponentPropsWithoutRef<typeof TabsPrimitive.List>) {
	return (
		<TabsPrimitive.List
			className={cn(
				"inline-flex h-9 w-fit items-center justify-center gap-1 rounded-full bg-muted p-1 text-muted-foreground",
				className,
			)}
			{...props}
		/>
	);
}

export function TabsTrigger({ className, ...props }: React.ComponentPropsWithoutRef<typeof TabsPrimitive.Trigger>) {
	return (
		<TabsPrimitive.Trigger
			className={cn(
				"inline-flex h-[calc(100%-1px)] flex-1 items-center justify-center gap-2 whitespace-nowrap rounded-full border border-transparent px-3 py-1 text-sm font-medium text-foreground/60 transition-[background-color,border-color,color,box-shadow] hover:text-foreground focus-visible:outline-none disabled:pointer-events-none disabled:opacity-50 data-[state=active]:bg-background data-[state=active]:text-foreground dark:text-muted-foreground dark:hover:text-foreground dark:data-[state=active]:border-input dark:data-[state=active]:bg-input/30 dark:data-[state=active]:text-foreground",
				className,
			)}
			{...props}
		/>
	);
}

export function TabsContent({ className, ...props }: React.ComponentPropsWithoutRef<typeof TabsPrimitive.Content>) {
	return <TabsPrimitive.Content className={cn("flex-1 text-sm outline-none", className)} {...props} />;
}
