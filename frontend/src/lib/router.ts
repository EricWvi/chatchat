import { useSyncExternalStore } from "react";

/*
 * Three kinds of page, each with a real path so chat links work:
 *   /            home: the new chat form
 *   /c/<chat_id> a chat, or its destroyed or not-found page
 *   /tokens      the user tokens
 */
export type Route = { page: "home" } | { page: "chat"; chatId: string } | { page: "tokens" };

function parse(path: string): Route {
  const m = /^\/c\/([^/]+)\/?$/.exec(path);
  if (m?.[1]) return { page: "chat", chatId: decodeURIComponent(m[1]) };
  if (path === "/tokens") return { page: "tokens" };
  return { page: "home" };
}

const listeners = new Set<() => void>();
let current = window.location.pathname;

function update() {
  current = window.location.pathname;
  for (const l of listeners) l();
}
window.addEventListener("popstate", update);

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

export function useRoute(): Route {
  const path = useSyncExternalStore(subscribe, () => current);
  return parse(path);
}

export function navigate(path: string, replace = false) {
  if (path === window.location.pathname) return;
  if (replace) history.replaceState(null, "", path);
  else history.pushState(null, "", path);
  update();
}

export const chatPath = (chatId: string) => `/c/${encodeURIComponent(chatId)}`;
