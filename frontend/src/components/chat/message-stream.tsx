import { useEffect, useLayoutEffect, useRef } from "react";

import { Avatar, ShortId } from "@/components/avatar";
import { Markdown } from "@/components/markdown";
import type { Message, Participant } from "@/lib/api";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";

// Within this distance of the bottom counts as following new messages.
const FOLLOW_PX = 40;
// Older messages load once scrolled this close to the top.
const OLDER_PX = 300;

/**
 * The message flow. It opens at the bottom and follows new messages while
 * the reader stays there; scrolled up, it keeps still. Older messages load
 * when the top comes near, without moving what is on screen.
 */
export function MessageStream({
  messages,
  participants,
  hasOlder,
  loadingOlder,
  onLoadOlder,
  stickToken,
}: {
  messages: Message[];
  participants: Map<string, Participant>;
  hasOlder: boolean;
  loadingOlder: boolean;
  onLoadOlder: () => void;
  /** Changes when the reader sent a message; jumps to the bottom. */
  stickToken: number;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const following = useRef(true);
  const prev = useRef({ first: 0, height: 0 });

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const first = messages[0]?.sequence ?? 0;
    if (prev.current.first !== 0 && first < prev.current.first) {
      // Older messages went in above: keep the same messages on screen.
      el.scrollTop += el.scrollHeight - prev.current.height;
    } else if (following.current) {
      el.scrollTop = el.scrollHeight;
    }
    prev.current = { first, height: el.scrollHeight };
  }, [messages]);

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el || stickToken === 0) return;
    following.current = true;
    el.scrollTop = el.scrollHeight;
  }, [stickToken]);

  // A short first page leaves nothing to scroll; load until it fills.
  useEffect(() => {
    const el = ref.current;
    if (el && hasOlder && !loadingOlder && el.scrollHeight <= el.clientHeight + OLDER_PX) onLoadOlder();
  }, [hasOlder, loadingOlder, messages, onLoadOlder]);

  const onScroll = () => {
    const el = ref.current;
    if (!el) return;
    following.current = el.scrollHeight - el.scrollTop - el.clientHeight < FOLLOW_PX;
    prev.current.height = el.scrollHeight;
    if (el.scrollTop < OLDER_PX && hasOlder && !loadingOlder) onLoadOlder();
  };

  const now = new Date();
  return (
    <div ref={ref} onScroll={onScroll} className="min-h-0 flex-1 overflow-auto px-6 py-5">
      {hasOlder && <div className="py-2 text-center text-[12.5px] text-ink-3">{loadingOlder ? "加载中…" : ""}</div>}
      {messages.map((m, i) => {
        const p = participants.get(m.participation_id);
        const cli = p?.kind === "cli";
        if (m.kind === "system") {
          return (
            <div key={m.sequence} className="py-1.5 pl-12 text-[12.5px] text-ink-3">
              {m.display_name} <ShortId id={m.participation_id} /> {m.text} · {formatTime(m.created_at, now)}
            </div>
          );
        }
        const before = messages[i - 1];
        const cont = before?.kind === "text" && before.participation_id === m.participation_id;
        return (
          <div
            key={m.sequence}
            className={cn("-mx-6 flex max-w-[868px] gap-3 px-6 py-2 hover:bg-hover", cont && "pt-0")}
          >
            <Avatar
              id={m.participation_id}
              name={m.display_name}
              cli={cli}
              className={cn(cont && "invisible h-0")}
            />
            <div className="min-w-0 flex-1">
              {!cont && (
                <div className="mb-px flex items-baseline gap-2">
                  <b className="font-bold">{m.display_name}</b>
                  {cli && <ShortId id={m.participation_id} />}
                  <span className="text-[13px] text-ink-3">{formatTime(m.created_at, now)}</span>
                  <span className="font-mono text-[12px] text-ink-3">#{m.sequence}</span>
                </div>
              )}
              <Markdown text={m.text} />
            </div>
          </div>
        );
      })}
    </div>
  );
}
