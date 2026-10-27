import { Tooltip as T } from "radix-ui";
import type * as React from "react";

/**
 * A short dark bubble on hover, after the data-tip hints of
 * design/web.html. The text may hold line breaks.
 */
export function Tooltip({
  content,
  side = "bottom",
  align = "center",
  children,
}: {
  content: React.ReactNode;
  side?: "top" | "bottom" | "left" | "right";
  align?: "start" | "center" | "end";
  children: React.ReactNode;
}) {
  return (
    <T.Root>
      <T.Trigger asChild>{children}</T.Trigger>
      <T.Portal>
        <T.Content
          side={side}
          align={align}
          sideOffset={6}
          collisionPadding={8}
          className="z-60 max-w-[280px] rounded-sm bg-ink px-2.5 py-1.5 text-[12px] leading-normal font-normal whitespace-pre-line text-bg data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=delayed-open]:animate-in data-[state=delayed-open]:fade-in-0"
        >
          {content}
        </T.Content>
      </T.Portal>
    </T.Root>
  );
}

export const TooltipProvider = T.Provider;
