import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { Button } from "@/components/ui/button";
import { Tooltip } from "@/components/ui/tooltip";
import { api } from "@/lib/api";
import { chatPath, navigate } from "@/lib/router";
import { notifyError } from "@/lib/toast";

export const fieldClass =
  "w-full rounded-field border-[0.5px] border-hairline-strong bg-surface px-3 py-2 transition-[border-color] duration-150 placeholder:text-ink-3 hover:border-[color-mix(in_oklch,var(--ink)_40%,transparent)] focus:border-[color-mix(in_oklch,var(--ink)_55%,transparent)] focus-visible:outline-none";
export const labelClass = "mt-3 mb-1 block text-[13px] text-ink-2";

/** The home page: the new chat form in the middle panel. */
export function NewChat() {
  const queryClient = useQueryClient();
  const [title, setTitle] = useState("");
  const [goal, setGoal] = useState("");
  const create = useMutation({
    mutationFn: () => api.createChat(title.trim(), goal.trim()),
    onSuccess: (c) => {
      void queryClient.invalidateQueries({ queryKey: ["chats"] });
      navigate(chatPath(c.chat_id));
    },
    onError: (err) => notifyError(err, "创建失败"),
  });

  return (
    <div className="grid flex-1 place-items-center rounded-panel border-[0.5px] border-hairline bg-surface p-6 shadow-panel">
      <form
        className="w-[min(440px,100%)]"
        onSubmit={(e) => {
          e.preventDefault();
          if (title.trim() && !create.isPending) create.mutate();
        }}
      >
        <h2 className="mb-4 text-[22px] font-extrabold">新建 chat</h2>
        <label className={`${labelClass} mt-0`} htmlFor="new-title">
          标题
        </label>
        <input
          id="new-title"
          className={fieldClass}
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          placeholder="例如：重构 auth 模块的方案讨论"
          maxLength={80}
          required
          autoFocus
        />
        <label className={labelClass} htmlFor="new-goal">
          沟通目标{" "}
          <Tooltip content="可选。会作为摘要「沟通目标」一节的起点">
            <span className="cursor-help text-ink-3">（可选）</span>
          </Tooltip>
        </label>
        <textarea
          id="new-goal"
          className={fieldClass}
          rows={3}
          value={goal}
          onChange={(e) => setGoal(e.target.value)}
          placeholder="这次要讨论清楚什么"
          maxLength={2000}
        />
        <div className="mt-4 flex justify-end">
          <Button type="submit" variant="primary" disabled={!title.trim() || create.isPending}>
            创建
          </Button>
        </div>
      </form>
    </div>
  );
}
