import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";
const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-md text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50",
  { variants: { variant: { default: "bg-primary text-primary-foreground shadow hover:bg-primary/90", outline: "border border-border bg-transparent shadow-sm hover:bg-accent", ghost: "hover:bg-accent", secondary: "bg-muted text-foreground hover:bg-muted/80" }, size: { default: "h-9 px-4 py-2", sm: "h-8 rounded-md px-3 text-xs", icon: "h-9 w-9" }, defaultVariants: { variant: "default", size: "default" } } }
);
export interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement>, VariantProps<typeof buttonVariants> {}
const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(({ className, variant, size, ...props }, ref) => (
  <button className={cn(buttonVariants({ variant, size, className }))} ref={ref} {...props} />
));
Button.displayName = "Button";
export { Button, buttonVariants };
