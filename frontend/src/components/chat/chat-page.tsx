import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { BotIcon, FlagIcon, LinkIcon, LogOutIcon, MoreHorizontalIcon, Trash2Icon } from "lucide-react";
import { useMemo, useState } from "react";

import { ChatPanel } from "@/components/chat/chat-panel";
import { Composer } from "@/components/chat/composer";
import { MessageStream } from "@/components/chat/message-stream";
import { DestroyedPage, NotFoundPage } from "@/components/state-pages";
import { Button, IconButton } from "@/components/ui/button";
import { Dialog, DialogActions } from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/menu";
import { Tooltip } from "@/components/ui/tooltip";
import { useChatFeed } from "@/hooks/use-chat-feed";
import { api, isCode, type ChatState, type Message } from "@/lib/api";
import { chatPath, navigate } from "@/lib/router";
import { notify, notifyError } from "@/lib/toast";

/**
 * A chat, with the sidebar around it drawn by the caller. Destroyed and
 * missing chats take the whole window, so the page reports them through
 * `standalone`.
 */
export function useChatPage(chatId: string) {
  const { feed, loadOlder, addMessage, markDestroyed, markLeaving } = useChatFeed(chatId);
  const [stickToken, setStickToken] = useState(0);
  const participants = useMemo(
    () => new Map(feed.state?.participants.map((p) => [p.participation_id, p])),
    [feed.state?.participants],
  );

  if (feed.phase === "destroyed") return { standalone: <DestroyedChat chatId={chatId} /> };
  if (feed.phase === "notfound") return { standalone: <NotFoundPage /> };

  const state = feed.state;
  const latestSequence = Math.max(state?.chat.latest_sequence ?? 0, feed.messages.at(-1)?.sequence ?? 0);
  const main =
    feed.phase === "ready" && state ? (
      <>
        <TopBar state={state} onDestroyed={markDestroyed} onLeaving={markLeaving} onMessage={addMessage} />
        <MessageStream
          messages={feed.messages}
          participants={participants}
          hasOlder={feed.hasOlder}
          loadingOlder={feed.loadingOlder}
          onLoadOlder={() => void loadOlder()}
          stickToken={stickToken}
        />
        <Composer
          key={chatId}
          chatId={chatId}
          title={state.chat.title}
          onSent={(m) => {
            addMessage(m);
            setStickToken((n) => n + 1);
          }}
        />
      </>
    ) : (
      <div className="grid flex-1 place-items-center text-[13px] text-ink-3">
        {feed.phase === "error" ? `${feed.error}，正在重试…` : ""}
      </div>
    );
  const panel = state ? <ChatPanel state={state} latestSequence={latestSequence} /> : <aside />;
  return { main, panel };
}

/** The destroyed page, with the chat's title, reason and time. */
function DestroyedChat({ chatId }: { chatId: string }) {
  const chat = useQuery({ queryKey: ["chat", chatId], queryFn: () => api.chat(chatId) });
  if (chat.data) return <DestroyedPage chat={chat.data} />;
  if (isCode(chat.error, "chat_not_found")) return <NotFoundPage />;
  return null;
}

function copy(text: string, done: string) {
  navigator.clipboard.writeText(text).then(
    () => notify(done),
    () => notify("复制失败，浏览器没有允许访问剪贴板"),
  );
}

function agentPrompt(state: ChatState) {
  const { chat_id, title } = state.chat;
  return `请阅读 ${window.location.origin}/agent.md，按指南用 chatchat CLI 加入 chat ${chat_id}（${title}），参与讨论，结束前 leave。`;
}

function TopBar({
  state,
  onDestroyed,
  onLeaving,
  onMessage,
}: {
  state: ChatState;
  onDestroyed: () => void;
  onLeaving: () => void;
  onMessage: (m: Message) => void;
}) {
  const queryClient = useQueryClient();
  const [confirm, setConfirm] = useState<"leave" | "destroy" | null>(null);
  const chatId = state.chat.chat_id;

  const proposeEnd = useMutation({
    mutationFn: () => api.proposeEnd(chatId),
    onSuccess: onMessage,
    onError: (err) => notifyError(err, "操作失败"),
  });
  const leave = useMutation({
    mutationFn: () => {
      onLeaving();
      return api.leave(chatId);
    },
    onSuccess: () => {
      setConfirm(null);
      void queryClient.invalidateQueries({ queryKey: ["chats"] });
      notify("已退出");
      navigate("/");
    },
    onError: (err) => notifyError(err, "退出失败"),
  });
  const destroy = useMutation({
    mutationFn: () => api.destroy(chatId),
    onSuccess: () => {
      setConfirm(null);
      onDestroyed();
    },
    onError: (err) => notifyError(err, "销毁失败"),
  });

  return (
    <div className="flex h-[72px] flex-none items-center gap-2.5 border-b border-line pr-4 pl-6">
      <h2 className="min-w-0 flex-1 truncate text-[20px] font-extrabold tracking-[-0.01em]">{state.chat.title}</h2>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button size="sm" className="data-[state=open]:bg-hover">
            <LinkIcon />
            邀请
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuLabel>邀请</DropdownMenuLabel>
          <DropdownMenuItem onSelect={() => copy(window.location.origin + chatPath(chatId), "已复制 chat 链接")}>
            <LinkIcon />
            复制 chat 链接
          </DropdownMenuItem>
          <Tooltip content={"复制一段提示，粘贴给 Agent 即可。\n不含任何凭据，Agent 用自己用户配置里的 token 加入"} side="left">
            <DropdownMenuItem onSelect={() => copy(agentPrompt(state), "已复制给 Agent 的提示")}>
              <BotIcon />
              复制给 Agent 的提示
            </DropdownMenuItem>
          </Tooltip>
        </DropdownMenuContent>
      </DropdownMenu>
      <DropdownMenu>
        <Tooltip content="更多" align="end">
          <DropdownMenuTrigger asChild>
            <IconButton aria-label="更多">
              <MoreHorizontalIcon />
            </IconButton>
          </DropdownMenuTrigger>
        </Tooltip>
        <DropdownMenuContent align="end">
          <DropdownMenuItem onSelect={() => proposeEnd.mutate()}>
            <FlagIcon />
            建议结束
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => setConfirm("leave")}>
            <LogOutIcon />
            退出
          </DropdownMenuItem>
          {state.chat.created_by_me && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem danger onSelect={() => setConfirm("destroy")}>
                <Trash2Icon />
                销毁 chat
              </DropdownMenuItem>
            </>
          )}
        </DropdownMenuContent>
      </DropdownMenu>

      <Dialog
        open={confirm === "leave"}
        onOpenChange={(o) => !o && setConfirm(null)}
        title="退出这个 chat？"
        description="退出后仍可从链接重新进入，身份不变。"
      >
        <DialogActions>
          <Button onClick={() => setConfirm(null)}>取消</Button>
          <Button variant="primary" disabled={leave.isPending} onClick={() => leave.mutate()}>
            退出
          </Button>
        </DialogActions>
      </Dialog>
      <Dialog
        open={confirm === "destroy"}
        onOpenChange={(o) => !o && setConfirm(null)}
        title={`销毁「${state.chat.title}」？`}
        description="删除全部消息和摘要，所有参与者的凭据立即失效。不能撤销。"
      >
        <DialogActions>
          <Button onClick={() => setConfirm(null)}>取消</Button>
          <Button variant="dangerFill" disabled={destroy.isPending} onClick={() => destroy.mutate()}>
            销毁
          </Button>
        </DialogActions>
      </Dialog>
    </div>
  );
}
