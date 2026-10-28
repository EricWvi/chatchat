/*
 * The JSON API (docs/api.md). The page only uses the account and web
 * participant endpoints, authenticated by the login cookie. Times are RFC
 * 3339 strings, or null when unset.
 */

export type User = { user_id: string; name: string; email: string };

/** Server limits from GET /api/v1/meta; the page does not hard-code them. */
export type Limits = {
  presence_lease_seconds: number;
  idle_destroy_after_seconds: number;
  max_chat_lifetime_seconds: number;
  wait_max_seconds: number;
  page_size_default: number;
  page_size_max: number;
  message_max_chars: number;
  display_name_max_chars: number;
  summary_threshold: number;
};

export type DestroyReason = "creator" | "idle" | "expired";

/** A chat as the account's chat list shows it; the counts are null once destroyed. */
export type ChatItem = {
  chat_id: string;
  title: string;
  created_by_me: boolean;
  created_at: string;
  expires_at: string;
  online_count: number | null;
  active_count: number | null;
  last_message_at: string | null;
  idle_destroy_at: string | null;
  destroyed_at: string | null;
  destroy_reason: DestroyReason | null;
};

export type CreatedChat = { chat_id: string; title: string; goal: string; url: string };

export type Token = { token_id: string; name: string; created_at: string; last_used_at: string | null };

export type ParticipantKind = "web" | "cli";
export type ParticipantStatus = "online" | "offline" | "left";

export type Participant = {
  participation_id: string;
  kind: ParticipantKind;
  display_name: string;
  status: ParticipantStatus;
  joined_at: string;
  left_at: string | null;
  last_seen_at: string | null;
};

export type Message = {
  message_id: string;
  sequence: number;
  kind: "text" | "system";
  participation_id: string;
  display_name: string;
  text: string;
  created_at: string;
};

export type Summary = {
  revision: number;
  covers_through_sequence: number;
  content: string;
  model: string;
  created_at: string;
};

export type ChatState = {
  chat: {
    chat_id: string;
    title: string;
    goal: string;
    created_by_me: boolean;
    created_at: string;
    expires_at: string;
    idle_destroy_at: string | null;
    latest_sequence: number;
  };
  summary: Summary | null;
  summary_job: { status: "idle" | "pending" | "failed"; last_failed_at: string | null };
  participants: Participant[];
  you: { participation_id: string };
  state_version: string;
};

export type MessagePage = { messages: Message[]; has_more: boolean; latest_sequence: number };

export type WaitResult = MessagePage & {
  changed: ("messages" | "summary" | "participants")[];
  state_version: string;
};

export class ApiError extends Error {
  readonly status: number;
  /** The stable error code, such as chat_destroyed; "" without a response. */
  readonly code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

export const isCode = (err: unknown, code: string) => err instanceof ApiError && err.code === code;

/** What the site says for each API error code; the server's `error` text is English, for the CLI. */
const errorText: Record<string, string> = {
  invalid_request: "输入不符合要求",
  unauthenticated: "登录已失效，请重新登录",
  token_revoked: "用户 token 已吊销",
  invalid_credentials: "参与凭据无效",
  chat_mismatch: "参与凭据属于另一个 chat",
  participation_left: "你已退出这个 chat",
  chat_destroyed: "chat 已销毁",
  wrong_credential_type: "凭据类型不对",
  forbidden: "没有权限执行这个操作",
  untrusted_origin: "请求来源不受信任",
  chat_not_found: "没有这个 chat",
  not_found: "要找的内容不存在",
  request_id_conflict: "同一条消息重复提交，但内容不同",
  too_large: "内容超过长度上限",
  internal: "服务器内部错误，请稍后重试",
};

async function request<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const init: RequestInit = { method, cache: "no-store", signal };
  if (body !== undefined) {
    init.body = JSON.stringify(body);
    init.headers = { "Content-Type": "application/json" };
  }
  let res: Response;
  try {
    res = await fetch(path, init);
  } catch (err) {
    if (signal?.aborted) throw err;
    throw new ApiError(0, "", navigator.onLine ? "无法连接服务器" : "当前处于离线状态");
  }
  if (res.status === 204) return undefined as T;
  const data: unknown = await res.json().catch(() => null);
  if (!res.ok) {
    const code = (data as { code?: string } | null)?.code ?? "";
    throw new ApiError(res.status, code, errorText[code] ?? `请求失败（${res.status}）`);
  }
  return data as T;
}

const web = (chatId: string) => `/api/v1/web/chats/${encodeURIComponent(chatId)}`;

export const api = {
  meta: () => request<{ protocol_version: number; limits: Limits }>("GET", "/api/v1/meta"),
  me: () => request<User>("GET", "/api/v1/me"),

  chats: () => request<{ chats: ChatItem[] }>("GET", "/api/v1/chats?limit=200").then((r) => r.chats),
  chat: (id: string) => request<ChatItem>("GET", `/api/v1/chats/${encodeURIComponent(id)}`),
  createChat: (title: string, goal: string) => request<CreatedChat>("POST", "/api/v1/chats", { title, goal }),

  tokens: () => request<{ tokens: Token[] }>("GET", "/api/v1/tokens").then((r) => r.tokens),
  createToken: (name: string) => request<Token & { token: string }>("POST", "/api/v1/tokens", { name }),
  revokeToken: (id: string) => request<void>("DELETE", `/api/v1/tokens/${encodeURIComponent(id)}`),

  enter: (chatId: string) => request<Participant>("POST", `${web(chatId)}/enter`),
  state: (chatId: string) => request<ChatState>("GET", `${web(chatId)}/state`),
  /** The last `limit` messages before `before`, oldest first. */
  messagesBefore: (chatId: string, before: number, limit = 50) =>
    request<MessagePage>("GET", `${web(chatId)}/messages?before_sequence=${before}&limit=${limit}`),
  /**
   * Long-polls for messages after `after`. It also returns early when the
   * summary or the participants changed since `stateVersion`.
   */
  wait: (chatId: string, after: number, stateVersion: string, signal: AbortSignal) =>
    request<WaitResult>(
      "GET",
      `${web(chatId)}/messages/wait?after_sequence=${after}&state_version=${encodeURIComponent(stateVersion)}`,
      undefined,
      signal,
    ),
  send: (chatId: string, text: string, clientRequestId: string) =>
    request<Message>("POST", `${web(chatId)}/messages`, { text, client_request_id: clientRequestId }),
  proposeEnd: (chatId: string) => request<Message>("POST", `${web(chatId)}/propose-end`, {}),
  leave: (chatId: string) => request<unknown>("POST", `${web(chatId)}/leave`),
  destroy: (chatId: string) => request<void>("DELETE", web(chatId)),
};

/** Ends this browser's session on the server. */
export async function logout() {
  let res: Response;
  try {
    res = await fetch("/auth/logout", { method: "POST", cache: "no-store" });
  } catch {
    throw new ApiError(0, "", "当前处于离线状态，无法退出登录");
  }
  if (!res.ok) throw new ApiError(res.status, "", "退出登录失败");
}
