import { useLayoutEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { useLimits } from "@/hooks/use-limits";
import { api, type Message } from "@/lib/api";
import { notify, notifyError } from "@/lib/toast";
import { randomId } from "@/lib/utils";

// The box grows with the text up to this height, then scrolls.
const MAX_HEIGHT = 240;

/**
 * The message box. Enter sends and Shift+Enter breaks the line; Enter
 * while an input method is composing does nothing. A failed send keeps the
 * text, and sending the same text again reuses its client_request_id, so
 * a send that did reach the server is not repeated.
 */
export function Composer({ chatId, title, onSent }: { chatId: string; title: string; onSent: (m: Message) => void }) {
  const limits = useLimits();
  const [text, setText] = useState("");
  const [sending, setSending] = useState(false);
  const pending = useRef<{ text: string; id: string } | null>(null);
  const ref = useRef<HTMLTextAreaElement>(null);

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, MAX_HEIGHT)}px`;
  }, [text]);

  const send = async () => {
    const body = text.trim();
    if (!body || sending) return;
    if (Array.from(body).length > limits.message_max_chars) {
      notify(`消息超过 ${limits.message_max_chars} 字，请删减后再发`);
      return;
    }
    const id = pending.current?.text === body ? pending.current.id : randomId();
    pending.current = { text: body, id };
    setSending(true);
    try {
      const m = await api.send(chatId, body, id);
      pending.current = null;
      setText("");
      onSent(m);
    } catch (err) {
      notifyError(err, "发送失败");
    } finally {
      setSending(false);
      ref.current?.focus();
    }
  };

  return (
    <div className="flex-none px-6 pt-3 pb-5">
      <div className="max-w-[820px] rounded-lg border-[0.5px] border-hairline-strong bg-surface pt-3.5 pr-3.5 pb-2.5 pl-4 shadow-input transition-shadow duration-200 focus-within:shadow-input-hover hover:shadow-input-hover">
        <textarea
          ref={ref}
          rows={1}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key !== "Enter" || e.shiftKey || e.nativeEvent.isComposing || e.keyCode === 229) return;
            e.preventDefault();
            void send();
          }}
          placeholder={`发消息给 ${title}`}
          className="block min-h-11 w-full bg-transparent p-0 placeholder:text-ink-3 focus-visible:outline-none"
        />
        <div className="mt-1.5 flex items-center justify-between text-[12px] text-ink-3">
          <span>Enter 发送，Shift+Enter 换行</span>
          <Button size="sm" variant="primary" disabled={!text.trim() || sending} onClick={() => void send()}>
            发送
          </Button>
        </div>
      </div>
    </div>
  );
}
