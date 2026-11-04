/**
 * use-current-user.ts：读取当前登录用户的 Hook（含回退默认值）。
 * 主要函数/类型：
 *   - useCurrentUser：从 auth 解析当前用户，未登录时返回内置回退用户
 */
"use client";

import { useEffect, useState } from "react";

import { auth, type CurrentUser } from "@/lib/auth";

const FALLBACK: CurrentUser = { id: "1", name: "artex", username: "artex", email: "", avatar: "", role: "operator" };

export function useCurrentUser(): CurrentUser {
  const [user, setUser] = useState<CurrentUser>(FALLBACK);

  useEffect(() => {
    const u = auth.getCurrentUser();
    if (u) setUser(u);
  }, []);

  return user;
}
