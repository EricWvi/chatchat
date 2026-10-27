import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronLeftIcon, CopyIcon, KeyRoundIcon, PlusIcon } from "lucide-react";
import { useState } from "react";

import { fieldClass, labelClass } from "@/components/new-chat";
import { Button, IconButton } from "@/components/ui/button";
import { Dialog, DialogActions } from "@/components/ui/dialog";
import { api, type Token } from "@/lib/api";
import { navigate } from "@/lib/router";
import { formatAgo, formatTime } from "@/lib/time";
import { notify, notifyError } from "@/lib/toast";

/** The user tokens page: list, generate (shown once) and revoke. */
export function TokensPage() {
  const tokens = useQuery({ queryKey: ["tokens"], queryFn: api.tokens });
  const [creating, setCreating] = useState(false);
  const [revoking, setRevoking] = useState<Token | null>(null);

  return (
    <div className="mx-auto my-6 max-w-[680px] rounded-panel border-[0.5px] border-hairline bg-surface px-10 pt-8 pb-12 shadow-panel">
      <a
        href="/"
        onClick={(e) => {
          e.preventDefault();
          navigate("/");
        }}
        className="mb-5 inline-flex items-center gap-1.5 text-[13px] text-ink-2 hover:text-ink [&_svg]:size-[18px]"
      >
        <ChevronLeftIcon />
        返回
      </a>
      <h1 className="mb-5 text-[24px] font-extrabold">用户 token</h1>
      <p className="-mt-3 mb-4 text-ink-2">填进本地 chatchat CLI，用于创建和加入 chat。不能代替参与凭据发消息。</p>
      <div className="mb-1 flex justify-end">
        <Button size="sm" onClick={() => setCreating(true)}>
          <PlusIcon />
          生成 token
        </Button>
      </div>
      {tokens.error && <p className="py-3 text-ink-3">{tokens.error.message}</p>}
      {tokens.data?.length === 0 && <p className="py-3 text-ink-3">还没有 token</p>}
      {tokens.data?.map((t) => (
        <div key={t.token_id} className="flex items-center gap-3 border-b border-line py-3 [&>svg]:size-[18px]">
          <KeyRoundIcon className="text-ink-2" />
          <div className="flex-1">
            <b className="block font-medium">{t.name}</b>
            <span className="text-[12.5px] text-ink-3">
              {formatTime(t.created_at)} 生成 · {t.last_used_at ? `${formatAgo(t.last_used_at)}使用` : "从未使用"}
            </span>
          </div>
          <Button size="sm" variant="danger" onClick={() => setRevoking(t)}>
            吊销
          </Button>
        </div>
      ))}
      <div className="mt-7 overflow-auto rounded-card bg-chip px-4 py-3.5 font-mono text-[12.5px] whitespace-pre">
        {`# ~/.config/chatchat/config.yaml
server: ${window.location.origin}
token: cct_…
# 也可以：chatchat --server ${window.location.origin} config set-token`}
      </div>
      <CreateTokenDialog open={creating} onOpenChange={setCreating} />
      <RevokeTokenDialog token={revoking} onClose={() => setRevoking(null)} />
    </div>
  );
}

function CreateTokenDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const create = useMutation({
    mutationFn: () => api.createToken(name.trim()),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["tokens"] }),
    onError: (err) => notifyError(err, "生成失败"),
  });
  const close = (o: boolean) => {
    onOpenChange(o);
    if (!o) {
      // Forget the token once the dialog is gone.
      setName("");
      create.reset();
    }
  };

  if (create.data) {
    const token = create.data.token;
    return (
      <Dialog open={open} onOpenChange={close} title="token 已生成" description="只显示这一次，关闭后无法再查看。">
        <div className="mt-3 flex items-center gap-2 rounded-field border-[0.5px] border-hairline bg-chip py-2.5 pr-2 pl-3 font-mono text-[12.5px] break-all">
          <span className="flex-1">{token}</span>
          <IconButton
            aria-label="复制"
            onClick={() =>
              void navigator.clipboard.writeText(token).then(
                () => notify("已复制"),
                () => notify("复制失败，请手动选择复制"),
              )
            }
          >
            <CopyIcon />
          </IconButton>
        </div>
        <DialogActions>
          <Button variant="primary" onClick={() => close(false)}>
            完成
          </Button>
        </DialogActions>
      </Dialog>
    );
  }
  return (
    <Dialog open={open} onOpenChange={close} title="生成用户 token">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (name.trim() && !create.isPending) create.mutate();
        }}
      >
        <label className={labelClass} htmlFor="token-name">
          名称
        </label>
        <input
          id="token-name"
          className={fieldClass}
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="例如：MacBook"
          maxLength={64}
          autoFocus
        />
        <DialogActions>
          <Button onClick={() => close(false)}>取消</Button>
          <Button type="submit" variant="primary" disabled={!name.trim() || create.isPending}>
            生成
          </Button>
        </DialogActions>
      </form>
    </Dialog>
  );
}

function RevokeTokenDialog({ token, onClose }: { token: Token | null; onClose: () => void }) {
  const queryClient = useQueryClient();
  const revoke = useMutation({
    mutationFn: (id: string) => api.revokeToken(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["tokens"] });
      onClose();
    },
    onError: (err) => notifyError(err, "吊销失败"),
  });
  return (
    <Dialog
      open={token !== null}
      onOpenChange={(o) => !o && onClose()}
      title={`吊销「${token?.name ?? ""}」？`}
      description="用这个 token 的 CLI 将无法再创建或加入 chat。已加入的参与者不受影响。"
    >
      <DialogActions>
        <Button onClick={onClose}>取消</Button>
        <Button variant="dangerFill" disabled={revoke.isPending} onClick={() => token && revoke.mutate(token.token_id)}>
          吊销
        </Button>
      </DialogActions>
    </Dialog>
  );
}
