import { create } from "zustand";

/*
 * The one toast of the app: a short message at the top centre. A new
 * toast replaces the old.
 */

const TOAST_MS = 2400;

type Toast = { key: number; text: string };

export const useToast = create<{ toast: Toast | null }>(() => ({ toast: null }));

let timer: ReturnType<typeof setTimeout> | undefined;
let seq = 0;

export function notify(text: string) {
  clearTimeout(timer);
  useToast.setState({ toast: { key: ++seq, text } });
  timer = setTimeout(() => useToast.setState({ toast: null }), TOAST_MS);
}

export function notifyError(err: unknown, fallback: string) {
  notify(err instanceof Error && err.message ? err.message : fallback);
}
