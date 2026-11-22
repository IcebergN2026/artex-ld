/**
 * skeleton.tsx：shadcn/ui 的 Skeleton 骨架屏占位组件封装。
 * 主要组件：Skeleton。
 */
import { cn } from "@/lib/utils"

function Skeleton({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="skeleton"
      className={cn("animate-pulse rounded-md bg-muted", className)}
      {...props}
    />
  )
}

export { Skeleton }
