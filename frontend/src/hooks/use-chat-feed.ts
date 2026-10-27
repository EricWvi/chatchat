import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useReducer, useRef } from "react";

import { api, isCode, type ChatState, type Message } from "@/lib/api";
import { navigate } from "@/lib/router";
import { goToLogin } from "@/lib/session";
import { notify } from "@/lib/toast";

/*
 * The live view of one chat: enter it, show the latest messages and keep
 * up with new ones, the summary and the participants through the web
 * long poll. The wait passes the state_version of the state on screen, so
 * changes made between two waits are not missed.
 */

export type Feed = {
  phase: "loading" | "ready" | "destroyed" | "notfound" | "error";
  error?: string;
  state?: ChatState;
  /** Oldest first, without gaps from the first loaded on. */
  messages: Message[];
  hasOlder: boolean;
  loadingOlder: boolean;
};

type Action =
  | { type: "reset" }
  | { type: "failed"; phase: Feed["phase"]; error?: string }
  | { type: "state"; state: ChatState }
  | { type: "loaded"; messages: Message[]; hasOlder: boolean }
  | { type: "append"; messages: Message[] }
  | { type: "olderStart" }
  | { type: "older"; messages: Message[]; hasOlder: boolean }
  | { type: "olderFailed" };

const initial: Feed = { phase: "loading", messages: [], hasOlder: false, loadingOlder: false };

/** Merges messages by sequence, oldest first. */
function merge(a: Message[], b: Message[]) {
  const bySeq = new Map<number, Message>();
  for (const m of a) bySeq.set(m.sequence, m);
  for (const m of b) bySeq.set(m.sequence, m);
  return [...bySeq.values()].sort((x, y) => x.sequence - y.sequence);
}

function reduce(feed: Feed, a: Action): Feed {
  switch (a.type) {
    case "reset":
      return initial;
    case "failed":
      return { ...feed, phase: a.phase, error: a.error };
    case "state":
      return { ...feed, state: a.state };
    case "loaded":
      return { ...feed, phase: "ready", messages: merge(feed.messages, a.messages), hasOlder: a.hasOlder };
    case "append":
      return { ...feed, messages: merge(feed.messages, a.messages) };
    case "olderStart":
      return { ...feed, loadingOlder: true };
    case "older":
      return { ...feed, messages: merge(a.messages, feed.messages), hasOlder: a.hasOlder, loadingOlder: false };
    case "olderFailed":
      return { ...feed, loadingOlder: false };
  }
}

const sleep = (ms: number, signal: AbortSignal) =>
  new Promise<void>((resolve) => {
    const t = setTimeout(resolve, ms);
    signal.addEventListener("abort", () => {
      clearTimeout(t);
      resolve();
    });
  });

export function useChatFeed(chatId: string) {
  const queryClient = useQueryClient();
  const [feed, dispatch] = useReducer(reduce, initial);
  // Set when this page left the chat itself, so the wait's
  // participation_left is not reported as leaving elsewhere.
  const leftHere = useRef(false);

  useEffect(() => {
    dispatch({ type: "reset" });
    leftHere.current = false;
    const ac = new AbortController();
    const { signal } = ac;
    let version = "";
    const refreshChats = () => void queryClient.invalidateQueries({ queryKey: ["chats"] });

    const loadState = async () => {
      const state = await api.state(chatId);
      version = state.state_version;
      if (!signal.aborted) dispatch({ type: "state", state });
      return state;
    };

    // Ends the feed for errors that settle the page; false for the rest.
    const settle = (err: unknown) => {
      if (signal.aborted) return true;
      if (isCode(err, "unauthenticated")) {
        goToLogin();
        return true;
      }
      if (isCode(err, "chat_not_found")) {
        dispatch({ type: "failed", phase: "notfound" });
        return true;
      }
      if (isCode(err, "chat_destroyed")) {
        dispatch({ type: "failed", phase: "destroyed" });
        refreshChats();
        return true;
      }
      if (isCode(err, "participation_left")) {
        if (!leftHere.current) {
          notify("你已在其他页面退出这个 chat");
          navigate("/");
        }
        refreshChats();
        return true;
      }
      return false;
    };

    void (async () => {
      let after = 0;
      // Enter, read the state and the latest page; retried until it works.
      for (let failures = 0; ; failures++) {
        try {
          await api.enter(chatId);
          refreshChats();
          const state = await loadState();
          const page = await api.messagesBefore(chatId, state.chat.latest_sequence + 1);
          if (signal.aborted) return;
          dispatch({ type: "loaded", messages: page.messages, hasOlder: page.has_more });
          after = page.messages.at(-1)?.sequence ?? state.chat.latest_sequence;
          break;
        } catch (err) {
          if (settle(err)) return;
          dispatch({ type: "failed", phase: "error", error: err instanceof Error ? err.message : "加载失败" });
          await sleep(Math.min(1000 * 2 ** failures, 15_000), signal);
          if (signal.aborted) return;
        }
      }

      // Long-poll until the page goes away. Each wait also keeps this
      // participant online.
      for (let failures = 0; !signal.aborted; ) {
        try {
          const r = await api.wait(chatId, after, version, signal);
          failures = 0;
          if (r.messages.length > 0) {
            dispatch({ type: "append", messages: r.messages });
            after = r.messages.at(-1)!.sequence;
          }
          version = r.state_version;
          if (r.changed.includes("summary") || r.changed.includes("participants")) await loadState();
          if (r.changed.includes("participants")) refreshChats();
        } catch (err) {
          if (settle(err)) return;
          if (isCode(err, "forbidden")) {
            // The participant is gone from the server's view; enter again.
            await api.enter(chatId).catch(() => undefined);
          }
          failures++;
          await sleep(Math.min(1000 * 2 ** failures, 15_000), signal);
        }
      }
    })();

    return () => ac.abort();
  }, [chatId, queryClient]);

  const firstSeq = feed.messages[0]?.sequence;
  const { hasOlder, loadingOlder } = feed;
  const loadOlder = useCallback(async () => {
    if (!hasOlder || loadingOlder || firstSeq === undefined) return;
    dispatch({ type: "olderStart" });
    try {
      const page = await api.messagesBefore(chatId, firstSeq);
      dispatch({ type: "older", messages: page.messages, hasOlder: page.has_more });
    } catch {
      dispatch({ type: "olderFailed" });
    }
  }, [chatId, firstSeq, hasOlder, loadingOlder]);

  /** Adds a message this page created, ahead of the wait reporting it. */
  const addMessage = useCallback((m: Message) => dispatch({ type: "append", messages: [m] }), []);

  const markDestroyed = useCallback(() => {
    dispatch({ type: "failed", phase: "destroyed" });
    void queryClient.invalidateQueries({ queryKey: ["chats"] });
  }, [queryClient]);

  const markLeaving = useCallback(() => {
    leftHere.current = true;
  }, []);

  return { feed, loadOlder, addMessage, markDestroyed, markLeaving };
}
