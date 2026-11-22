/**
 * page.tsx：单个 Agent 的深度链接编辑页（复用 /system/agents 抽屉里的 AgentEditor，全宽展示）。
 * 主要函数/类型：
 *   - AgentDetailPage：页面出口，用 Suspense 包裹以满足静态导出要求
 *   - AgentDetailInner：读取 URL 的 ?key= 参数并渲染 AgentEditor
 */
"use client";

import * as React from "react";

import Link from "next/link";
import { useSearchParams } from "next/navigation";

import { ArrowLeftIcon } from "lucide-react";

import { AgentEditor } from "@/components/agent-editor";
import { Button } from "@/components/ui/button";

// Deep-link editor page for one agent. The primary flow is the drawer on
// /system/agents; this page reuses the same AgentEditor component full-width so a
// direct URL (or an external link) still opens the editor.
function AgentDetailInner() {
  const searchParams = useSearchParams();
  const key = searchParams.get("key") ?? "";

  return (
    <div className="flex flex-1 flex-col gap-4">
      <div className="flex items-center gap-2">
        <Button asChild variant="ghost" size="icon-sm">
          <Link href="/system/agents">
            <ArrowLeftIcon className="size-4" />
          </Link>
        </Button>
        <div>
          <h1 className="text-xl font-semibold tracking-tight">Agent</h1>
          <p className="text-muted-foreground font-mono text-xs">{key}</p>
        </div>
      </div>
      <div className="flex min-h-0 flex-1 flex-col rounded-lg border">
        <AgentEditor agentKey={key} />
      </div>
    </div>
  );
}

// useSearchParams must sit under a Suspense boundary for static export.
export default function AgentDetailPage() {
  return (
    <React.Suspense fallback={null}>
      <AgentDetailInner />
    </React.Suspense>
  );
}
