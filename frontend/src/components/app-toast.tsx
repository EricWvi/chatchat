import { useToast } from "@/lib/toast";

/** The toast at the top centre of the window. */
export function AppToast() {
  const toast = useToast((s) => s.toast);
  if (!toast) return null;
  return (
    <div
      key={toast.key}
      role="status"
      className="fixed top-[18px] left-1/2 z-70 -translate-x-1/2 animate-in rounded-card border-[0.5px] border-hairline bg-surface px-3.5 py-[9px] text-[13.5px] whitespace-nowrap shadow-md fade-in-0 slide-in-from-top-1"
    >
      {toast.text}
    </div>
  );
}
