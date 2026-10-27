import { Button } from "@/components/ui/button";
import type { ChatItem, DestroyReason } from "@/lib/api";
import { navigate } from "@/lib/router";
import { formatTime } from "@/lib/time";

export const destroyReasons: Record<DestroyReason, string> = {
  creator: "创建者销毁",
  idle: "无人在线 30 分钟后自动销毁",
  expired: "满 24 小时自动销毁",
};

function StatePage({ glyph, title, children }: { glyph: string; title: string; children: React.ReactNode }) {
  return (
    <div className="grid min-h-full place-items-center p-5 text-center">
      <div>
        <div className="mb-2.5 font-mono text-[13px] text-ink-3">{glyph}</div>
        <h1 className="mb-2 text-[22px] font-extrabold">{title}</h1>
        <div className="mb-5 text-ink-2">{children}</div>
        <Button onClick={() => navigate("/")}>回到首页</Button>
      </div>
    </div>
  );
}

export function DestroyedPage({ chat }: { chat: ChatItem }) {
  return (
    <StatePage glyph={chat.chat_id} title="这个 chat 已销毁">
      {chat.title}
      {chat.destroy_reason && ` · ${destroyReasons[chat.destroy_reason]}`}
      {chat.destroyed_at && ` · ${formatTime(chat.destroyed_at)}`}
      <br />
      <span className="text-ink-3">消息和摘要已删除，所有参与凭据已失效</span>
    </StatePage>
  );
}

export function NotFoundPage() {
  return (
    <StatePage glyph="404" title="没有这个 chat">
      链接可能不完整，请向邀请你的人再要一次
    </StatePage>
  );
}
