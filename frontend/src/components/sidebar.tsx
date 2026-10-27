import { PlusIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { UserMenu } from "@/components/user-menu";
import { useChats } from "@/hooks/use-chats";
import type { ChatItem, User } from "@/lib/api";
import { chatPath, navigate } from "@/lib/router";
import { cn } from "@/lib/utils";

/** The left column: new chat, the chat list and the user menu. */
export function Sidebar({ user, currentId }: { user: User; currentId?: string }) {
  const chats = useChats();
  const active = chats.data?.filter((c) => !c.destroyed_at) ?? [];
  const gone = chats.data?.filter((c) => c.destroyed_at) ?? [];

  return (
    <aside className="flex min-h-0 flex-col">
      <div className="flex items-center gap-2 px-4 pt-4 pb-3 font-bold tracking-[-0.01em]">
        <span className="grid size-7 place-items-center rounded-sm bg-accent text-[12px] text-white">c</span>
        chatchat
      </div>
      <Button className="mx-3 mt-1 mb-3" onClick={() => navigate("/")}>
        <PlusIcon />
        新建 chat
      </Button>
      <nav className="min-h-0 flex-1 overflow-auto px-2">
        {chats.error && (
          <p className="px-2 py-3 text-[13px] text-ink-3">
            {chats.error.message}，
            <button className="cursor-pointer text-link" onClick={() => void chats.refetch()}>
              重试
            </button>
          </p>
        )}
        {active.length > 0 && <Group label="进行中" />}
        {active.map((c) => (
          <Row key={c.chat_id} chat={c} current={c.chat_id === currentId} />
        ))}
        {gone.length > 0 && <Group label="已销毁" />}
        {gone.map((c) => (
          <Row key={c.chat_id} chat={c} current={c.chat_id === currentId} />
        ))}
      </nav>
      <UserMenu user={user} />
    </aside>
  );
}

function Group({ label }: { label: string }) {
  return <div className="px-2 pt-3 pb-1 text-[12px] text-ink-3">{label}</div>;
}

function Row({ chat, current }: { chat: ChatItem; current: boolean }) {
  const destroyed = chat.destroyed_at !== null;
  const online = chat.online_count ?? 0;
  const path = chatPath(chat.chat_id);
  return (
    <a
      href={path}
      onClick={(e) => {
        if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
        e.preventDefault();
        navigate(path);
      }}
      className={cn(
        "flex items-center gap-2 rounded-sm px-2 py-[7px] hover:bg-row-hover",
        current && "bg-row-current font-semibold hover:bg-row-current",
      )}
    >
      <StatusDot status={destroyed ? "left" : online > 0 ? "online" : "offline"} />
      <span className={cn("min-w-0 flex-1 truncate", destroyed && "text-ink-3")}>{chat.title}</span>
      {!destroyed && (
        <span className="text-[12px] font-normal text-ink-3 tabular-nums">
          {online}/{chat.active_count ?? 0}
        </span>
      )}
    </a>
  );
}

/** Green when online, a hollow ring when offline, faint grey when left or gone. */
export function StatusDot({ status, className }: { status: "online" | "offline" | "left"; className?: string }) {
  return (
    <span
      className={cn(
        "inline-block size-2 flex-none rounded-full",
        status === "online" && "bg-online",
        status === "offline" && "shadow-[inset_0_0_0_1.5px_var(--ink-3)]",
        status === "left" && "bg-line",
        className,
      )}
    />
  );
}
