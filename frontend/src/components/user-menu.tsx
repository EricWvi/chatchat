import { useQueryClient } from "@tanstack/react-query";
import { KeyRoundIcon, LogOutIcon } from "lucide-react";

import { Avatar } from "@/components/avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/menu";
import { logout, type User } from "@/lib/api";
import { navigate } from "@/lib/router";
import { useSession } from "@/lib/session";
import { notifyError } from "@/lib/toast";

export function UserMenu({ user }: { user: User }) {
  const queryClient = useQueryClient();
  const setSignedOut = useSession((s) => s.setSignedOut);
  const name = user.name || user.email;

  const signOut = async () => {
    try {
      await logout();
    } catch (err) {
      notifyError(err, "退出登录失败");
      return;
    }
    setSignedOut(true);
    queryClient.clear();
  };

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button className="m-2 flex cursor-pointer items-center gap-2 rounded-sm p-2 text-left font-semibold hover:bg-row-hover data-[state=open]:bg-row-hover">
          <Avatar id={user.user_id} name={name} size="sm" />
          <span className="min-w-0 flex-1 truncate">{name}</span>
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent side="top" align="start">
        {user.email && <DropdownMenuLabel>{user.email}</DropdownMenuLabel>}
        <DropdownMenuItem onSelect={() => navigate("/tokens")}>
          <KeyRoundIcon />
          用户 token
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={() => void signOut()}>
          <LogOutIcon />
          退出登录
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
