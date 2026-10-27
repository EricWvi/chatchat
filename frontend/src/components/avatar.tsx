import { cn } from "@/lib/utils";

const TINTS = ["bg-[var(--tint-1)]", "bg-[var(--tint-2)]", "bg-[var(--tint-3)]", "bg-[var(--tint-4)]", "bg-[var(--tint-5)]", "bg-[var(--tint-6)]"];

/** A stable tint for an ID. */
function tint(id: string) {
  let h = 0;
  for (const c of id) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  return TINTS[h % TINTS.length];
}

/**
 * A rounded-square avatar with the first letter of the name, tinted by ID.
 * Agent names often start with a bracket or a CJK word, so a CLI
 * participant shows its first Latin letter, or a dot.
 */
export function Avatar({
  id,
  name,
  cli = false,
  size = "md",
  className,
}: {
  id: string;
  name: string;
  cli?: boolean;
  size?: "md" | "sm";
  className?: string;
}) {
  const letter = cli ? (/^[A-Za-z]/.test(name) ? name[0]!.toUpperCase() : "·") : Array.from(name)[0] ?? "?";
  return (
    <span
      aria-hidden
      className={cn(
        "grid flex-none place-items-center font-semibold text-[#2b2b2e]",
        size === "md" ? "size-9 rounded-[9px] text-[12px]" : "size-6 rounded-[6px] text-[11px]",
        tint(id),
        className,
      )}
    >
      {letter}
    </span>
  );
}

/** The last four characters of a participation ID, to tell same-named participants apart. */
export function ShortId({ id, className }: { id: string; className?: string }) {
  return <span className={cn("font-mono text-[11px] text-ink-3", className)}>{id.slice(-4)}</span>;
}
