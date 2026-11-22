/**
 * account-switcher.tsx：顶栏头像账号菜单，展示当前用户并提供修改密码与退出登录入口。
 * 主要函数/类型：
 *   - AccountSwitcher：头像触发的下拉菜单组件。
 *   - AccountUser：传入用户的 id/name/email/avatar/role 结构。
 */
"use client";

import { useState } from "react";

import { Check, KeyRound, LogOut } from "lucide-react";

import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { auth } from "@/lib/auth";
import { cn, getInitials } from "@/lib/utils";

import { ChangePasswordDialog } from "./change-password-dialog";

interface AccountUser {
  readonly id: string;
  readonly name: string;
  readonly email: string;
  readonly avatar: string;
  readonly role: string;
}

// AccountSwitcher 是顶栏的单账号菜单：头像触发，展开显示当前用户、修改密码与退出登录。
// （原设计支持多账号切换，但唯一调用方固定传单用户；若恢复多账号，再把列表与选中态加回来。）
export function AccountSwitcher({ user }: { readonly user: AccountUser }) {
  const [pwOpen, setPwOpen] = useState(false);

  function handleLogout() {
    auth.clearToken();
    window.location.href = "/login";
  }

  if (!user) {
    return null;
  }

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Avatar className="size-8 rounded-lg">
            <AvatarImage src={user.avatar || undefined} alt={user.name} />
            <AvatarFallback>{getInitials(user.name)}</AvatarFallback>
          </Avatar>
        </DropdownMenuTrigger>
        <DropdownMenuContent className="min-w-56 space-y-1 rounded-lg" side="bottom" align="end" sideOffset={4}>
          <DropdownMenuItem className="bg-accent/50 p-0" aria-current="true">
            <div className="flex w-full items-center gap-2 px-1 py-1.5">
              <Avatar className="size-9 rounded-lg">
                <AvatarImage src={user.avatar || undefined} alt={user.name} />
                <AvatarFallback>{getInitials(user.name)}</AvatarFallback>
              </Avatar>
              <div className="grid min-w-0 flex-1 text-left text-sm leading-tight">
                <span className="truncate font-semibold">{user.name}</span>
                <span className="truncate text-xs capitalize">{user.role}</span>
              </div>
              <span className="mr-1 flex size-5 items-center justify-center rounded-full text-primary opacity-100">
                <Check aria-hidden="true" />
              </span>
            </div>
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem onClick={() => setPwOpen(true)}>
            <KeyRound />
            修改密码
          </DropdownMenuItem>
          <DropdownMenuItem onClick={handleLogout} className="text-destructive focus:text-destructive">
            <LogOut />
            退出登录
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <ChangePasswordDialog open={pwOpen} onOpenChange={setPwOpen} />
    </>
  );
}
