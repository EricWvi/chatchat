import { AppToast } from "@/components/app-toast";
import { useChatPage } from "@/components/chat/chat-page";
import { NewChat } from "@/components/new-chat";
import { Sidebar } from "@/components/sidebar";
import { TokensPage } from "@/components/tokens-page";
import { Button } from "@/components/ui/button";
import { TooltipProvider } from "@/components/ui/tooltip";
import { useMe } from "@/hooks/use-me";
import { ApiError, type User } from "@/lib/api";
import { useRoute } from "@/lib/router";
import { goToLogin, useSession } from "@/lib/session";

export function App() {
  return (
    <TooltipProvider delayDuration={200} skipDelayDuration={300}>
      <Pages />
      <AppToast />
    </TooltipProvider>
  );
}

function Pages() {
  const signedOut = useSession((s) => s.signedOut);
  const setSignedOut = useSession((s) => s.setSignedOut);
  const me = useMe();
  const route = useRoute();

  if (signedOut) {
    return (
      <main className="flex h-full flex-col items-center justify-center gap-4 text-ink-2">
        <p>已退出登录</p>
        <Button
          onClick={() => {
            setSignedOut(false);
            goToLogin();
          }}
        >
          登录
        </Button>
      </main>
    );
  }
  // A lost login is already on its way to the login page.
  if (me.isError && !me.isFetching && !(me.error instanceof ApiError && me.error.status === 401)) {
    return (
      <main className="flex h-full flex-col items-center justify-center gap-4 text-ink-2">
        <p>{me.error.message}</p>
        <Button onClick={() => void me.refetch()}>重试</Button>
      </main>
    );
  }
  if (!me.data) return null;

  if (route.page === "tokens") {
    return (
      <div className="h-full overflow-auto px-4">
        <TokensPage />
      </div>
    );
  }
  if (route.page === "chat") return <ChatLayout key={route.chatId} user={me.data} chatId={route.chatId} />;
  return (
    <div className="mx-auto grid h-full max-w-[1440px] grid-cols-[260px_1fr] py-2 pr-2">
      <Sidebar user={me.data} />
      <main className="ml-6 flex min-h-0 min-w-0 flex-col">
        <NewChat />
      </main>
    </div>
  );
}

/** Layout A: chat list, the chat, and its summary and participants. */
function ChatLayout({ user, chatId }: { user: User; chatId: string }) {
  const page = useChatPage(chatId);
  if ("standalone" in page) return <div className="h-full overflow-auto">{page.standalone}</div>;
  return (
    <div className="mx-auto grid h-full max-w-[1440px] grid-cols-[260px_1fr_320px] py-2 pr-2">
      <Sidebar user={user} currentId={chatId} />
      <main className="ml-6 flex min-h-0 min-w-0 flex-col rounded-l-panel border-[0.5px] border-r-0 border-hairline bg-surface shadow-panel">
        {page.main}
      </main>
      {page.panel}
    </div>
  );
}
