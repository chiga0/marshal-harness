import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";
const badgeVariants = cva("inline-flex items-center rounded-full border border-transparent px-2.5 py-0.5 text-xs font-semibold transition-colors",
  { variants: { variant: { default: "bg-muted text-foreground", ok: "bg-green-500/15 text-green-500", err: "bg-red-500/15 text-red-500", warn: "bg-yellow-500/15 text-yellow-500", info: "bg-blue-500/15 text-blue-500", mut: "bg-muted text-muted-foreground" }, defaultVariants: { variant: "default" } } }
);
export interface BadgeProps extends React.HTMLAttributes<HTMLDivElement>, VariantProps<typeof badgeVariants> {}
function Badge({ className, variant, ...props }: BadgeProps) { return <div className={cn(badgeVariants({ variant }), className)} {...props} />; }
export { Badge, badgeVariants };
