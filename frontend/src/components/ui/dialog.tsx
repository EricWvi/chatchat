import { Dialog as D } from "radix-ui";
import type * as React from "react";

import { skipFocusAfterPointer } from "@/lib/input-mode";
import { cn } from "@/lib/utils";

/** A centred dialog card over a scrim, after design/web.html. */
export function Dialog({
  open,
  onOpenChange,
  title,
  description,
  children,
  className,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: React.ReactNode;
  description?: React.ReactNode;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <D.Root open={open} onOpenChange={onOpenChange}>
      <D.Portal>
        <D.Overlay className="fixed inset-0 z-50 bg-scrim data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:animate-in data-[state=open]:fade-in-0" />
        <D.Content
          onCloseAutoFocus={skipFocusAfterPointer}
          className={cn(
            "fixed top-1/2 left-1/2 z-50 w-[min(480px,calc(100vw-32px))] -translate-x-1/2 -translate-y-1/2 rounded-panel border-[0.5px] border-hairline bg-surface p-6 shadow-md data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-[0.98]",
            className,
          )}
        >
          <D.Title className="mb-1 text-[20px] font-extrabold">{title}</D.Title>
          {description ? (
            <D.Description className="text-ink-2">{description}</D.Description>
          ) : (
            <D.Description className="sr-only">{title}</D.Description>
          )}
          {children}
        </D.Content>
      </D.Portal>
    </D.Root>
  );
}

export function DialogActions({ children }: { children: React.ReactNode }) {
  return <div className="mt-5 flex justify-end gap-2">{children}</div>;
}
