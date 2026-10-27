import { cva, type VariantProps } from "class-variance-authority";
import type * as React from "react";

import { cn } from "@/lib/utils";

const buttonVariants = cva(
  "inline-flex shrink-0 cursor-pointer items-center justify-center gap-1.5 rounded-sm border-[0.5px] font-medium whitespace-nowrap transition-[background-color,border-color,transform] duration-150 active:scale-[0.985] disabled:cursor-default disabled:opacity-45 disabled:active:scale-100 [&_svg]:size-[18px] [&_svg]:shrink-0",
  {
    variants: {
      variant: {
        default: "border-hairline-strong bg-transparent hover:bg-hover",
        primary: "border-transparent bg-primary text-on-primary hover:bg-primary-hover",
        ghost: "border-transparent bg-transparent hover:bg-hover",
        danger: "border-transparent bg-transparent text-danger hover:bg-hover",
        dangerFill:
          "border-transparent bg-danger text-white hover:bg-[color-mix(in_oklch,var(--danger),#000_12%)]",
      },
      size: {
        md: "h-9 px-3.5 text-[14px]",
        sm: "h-8 px-3 text-[13.5px]",
      },
    },
    defaultVariants: { variant: "default", size: "md" },
  },
);

export function Button({
  className,
  variant,
  size,
  ...props
}: React.ComponentProps<"button"> & VariantProps<typeof buttonVariants>) {
  return <button type="button" className={cn(buttonVariants({ variant, size }), className)} {...props} />;
}

/** A 32px square button holding one icon. */
export function IconButton({ className, ...props }: React.ComponentProps<"button">) {
  return (
    <button
      type="button"
      className={cn(
        "inline-grid size-8 shrink-0 cursor-pointer place-items-center rounded-sm text-ink-2 transition-[background-color,transform] duration-150 hover:bg-selected hover:text-ink active:scale-[0.985] data-[state=open]:bg-selected data-[state=open]:text-ink [&_svg]:size-[18px]",
        className,
      )}
      {...props}
    />
  );
}
