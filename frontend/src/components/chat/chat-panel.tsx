import { ClockIcon, HourglassIcon } from "lucide-react";
import { useEffect, useState } from "react";

import { Avatar, ShortId } from "@/components/avatar";
import { Markdown } from "@/components/markdown";
import { StatusDot } from "@/components/sidebar";
import { Tooltip } from "@/components/ui/tooltip";
import { useLimits } from "@/hooks/use-limits";
import type { ChatState, Limits, Participant } from "@/lib/api";
import { formatTime, minutesUntil } from "@/lib/time";
import { cn } from "@/lib/utils";

/** The right column: summary, participants and lifecycle. */
export function ChatPanel({ state, latestSequence }: { state: ChatState; latestSequence: number }) {
  const limits = useLimits();
  return (
    <aside className="flex min-h-0 flex-col overflow-auto rounded-r-panel border-[0.5px] border-hairline bg-surface px-5 pt-1 pb-5 shadow-panel">
      <Section first>
        <SummarySection state={state} latestSequence={latestSequence} limits={limits} />
      </Section>
      <Section>
        <People state={state} limits={limits} />
      </Section>
      <Section last>
        <h5 className="mb-2.5 text-[15px] font-bold">生命周期</h5>
        <Lifecycle state={state} limits={limits} />
      </Section>
    </aside>
  );
}

function Section({ first, last, children }: { first?: boolean; last?: boolean; children: React.ReactNode }) {
  return <section className={cn("border-b border-line py-[18px]", first && "pt-5", last && "border-b-0")}>{children}</section>;
}

function SummarySection({ state, latestSequence, limits }: { state: ChatState; latestSequence: number; limits: Limits }) {
  const s = state.summary;
  const job = state.summary_job.status;
  const tip = `服务端模型整理的进展：自上份摘要新增 ${limits.summary_threshold} 条消息，或有人退出时更新。\n摘要之后的消息需要另外看。`;
  return (
    <>
      <h5 className="mb-2.5 flex items-center justify-between text-[15px] font-bold">
        <Tooltip content={tip} align="start">
          <span className="cursor-help">摘要</span>
        </Tooltip>
        <span className="font-normal text-ink-3">r{s?.revision ?? 0}</span>
      </h5>
      <div className="rounded-card border-[0.5px] border-summary-line bg-summary px-3.5 py-3">
        <div className="flex flex-wrap items-center gap-x-1 text-[12.5px] text-ink-2">
          {s ? (
            <>
              整理到 <span className="tabular-nums">#{s.covers_through_sequence}</span> · 之后{" "}
              <span className="tabular-nums">{Math.max(0, latestSequence - s.covers_through_sequence)}</span> 条
              {job === "pending" && (
                <>
                  {" · "}
                  <Pending>生成中</Pending>
                </>
              )}
              {job === "failed" && (
                <>
                  {" · "}
                  <Failed at={state.summary_job.last_failed_at} />
                </>
              )}
            </>
          ) : job === "pending" ? (
            <Pending>正在生成第一份摘要</Pending>
          ) : (
            <>
              还没有摘要
              {job === "failed" && (
                <>
                  {" · "}
                  <Failed at={state.summary_job.last_failed_at} />
                </>
              )}
            </>
          )}
        </div>
        {s && <Markdown text={s.content} className="mt-2 text-[13px]" />}
      </div>
    </>
  );
}

function Pending({ children }: { children: React.ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1.5 before:size-1.5 before:animate-pulse-dot before:rounded-full before:bg-accent">
      {children}
    </span>
  );
}

function Failed({ at }: { at: string | null }) {
  const when = at ? `${formatTime(at)} ` : "";
  return (
    <Tooltip content={`${when}模型调用失败，下次满足条件时重试`}>
      <span className="cursor-help text-danger">生成失败</span>
    </Tooltip>
  );
}

function statusTip(p: Participant, limits: Limits) {
  switch (p.status) {
    case "online":
      return "在线";
    case "offline":
      return p.kind === "cli"
        ? `离线：超过 ${limits.presence_lease_seconds} 秒没有操作，凭据仍有效，再次操作即恢复在线`
        : `离线：超过 ${limits.presence_lease_seconds} 秒没有打开这个 chat`;
    case "left":
      return p.kind === "cli" ? "已退出：凭据已失效" : "已退出：再次打开 chat 即恢复";
  }
}

function People({ state, limits }: { state: ChatState; limits: Limits }) {
  const online = state.participants.filter((p) => p.status === "online").length;
  return (
    <>
      <h5 className="mb-2.5 flex items-center justify-between text-[15px] font-bold">
        参与者 <span className="font-normal text-ink-3 tabular-nums">{online} 在线</span>
      </h5>
      {state.participants.map((p) => {
        const me = p.participation_id === state.you.participation_id;
        const left = p.status === "left";
        return (
          <div key={p.participation_id} className="flex items-center gap-2 py-[5px]">
            <Avatar id={p.participation_id} name={p.display_name} cli={p.kind === "cli"} size="sm" />
            <span className="flex min-w-0 flex-1 items-baseline gap-1.5">
              <b className={cn("truncate font-bold", left && "font-normal text-ink-3")}>{p.display_name}</b>
              {me ? <span className="flex-none text-ink-3">（我）</span> : <ShortId id={p.participation_id} className="flex-none" />}
            </span>
            <span className="inline-flex h-5 flex-none items-center rounded-[6px] bg-chip px-[7px] text-[11.5px] font-medium text-ink-2">
              {p.kind === "web" ? "网页" : "CLI"}
            </span>
            <Tooltip content={statusTip(p, limits)} align="end">
              <span className="grid size-4 flex-none cursor-help place-items-center">
                <StatusDot status={p.status} />
              </span>
            </Tooltip>
          </div>
        );
      })}
    </>
  );
}

/** Re-renders every 30 seconds, for countdowns. */
function useNow() {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const t = setInterval(() => setNow(new Date()), 30_000);
    return () => clearInterval(t);
  }, []);
  return now;
}

function Lifecycle({ state, limits }: { state: ChatState; limits: Limits }) {
  const now = useNow();
  const idleAt = state.chat.idle_destroy_at;
  const idleMinutes = Math.round(limits.idle_destroy_after_seconds / 60);
  const lifetimeHours = Math.round(limits.max_chat_lifetime_seconds / 3600);
  return (
    <div className="flex flex-wrap gap-x-3.5 gap-y-1 text-[12.5px] text-ink-2 [&_svg]:size-[18px]">
      <Tooltip
        content={`没有在线参与者持续 ${idleMinutes} 分钟后自动销毁。\n在线：${limits.presence_lease_seconds} 秒内有过读取、等待或发送`}
        align="start"
      >
        <span className="inline-flex cursor-help items-center gap-[5px]">
          <ClockIcon />
          {idleAt ? `${minutesUntil(idleAt, now)} 分钟后空闲销毁` : "有人在线"}
        </span>
      </Tooltip>
      <Tooltip content={`创建满 ${lifetimeHours} 小时后自动销毁，不论是否有人在用`} align="start">
        <span className="inline-flex cursor-help items-center gap-[5px]">
          <HourglassIcon />
          最迟 {formatTime(state.chat.expires_at, now)} 销毁
        </span>
      </Tooltip>
    </div>
  );
}
