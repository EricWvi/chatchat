import { DropdownMenu as Menu } from "radix-ui";
import type * as React from "react";

import { skipFocusAfterPointer } from "@/lib/input-mode";
import { cn } from "@/lib/utils";

/*
 * The one menu of the app, after the invite, more and user menus of
 * design/web.html: padded rounded items on a surface card.
 */

export const DropdownMenu = Menu.Root;
export const DropdownMenuTrigger = Menu.Trigger;

export function DropdownMenuContent({
  className,
  sideOffset = 6,
  ...props
}: React.ComponentProps<typeof Menu.Content>) {
  return (
    <Menu.Portal>
      <Menu.Content
        sideOffset={sideOffset}
        collisionPadding={8}
        onCloseAutoFocus={skipFocusAfterPointer}
        className={cn(
          "z-40 min-w-[200px] rounded-card border-[0.5px] border-hairline bg-surface p-1.5 text-[15px] text-ink shadow-md data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:animate-in data-[state=open]:fade-in-0",
          className,
        )}
        {...props}
      />
    </Menu.Portal>
  );
}

export function DropdownMenuItem({
  className,
  danger,
  ...props
}: React.ComponentProps<typeof Menu.Item> & { danger?: boolean }) {
  return (
    <Menu.Item
      className={cn(
        "flex w-full cursor-pointer items-center gap-2.5 rounded-sm px-2.5 py-[7px] outline-none select-none data-[highlighted]:bg-selected [&_svg]:size-[18px] [&_svg]:shrink-0",
        danger && "text-danger",
        className,
      )}
      {...props}
    />
  );
}

export function DropdownMenuLabel({ className, ...props }: React.ComponentProps<typeof Menu.Label>) {
  return <Menu.Label className={cn("px-2.5 pt-1.5 pb-0.5 text-[12px] text-ink-3", className)} {...props} />;
}

export function DropdownMenuSeparator({ className, ...props }: React.ComponentProps<typeof Menu.Separator>) {
  return <Menu.Separator className={cn("mx-1 my-1.5 h-px bg-line", className)} {...props} />;
}
