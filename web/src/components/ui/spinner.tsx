/**
 * spinner.tsx：Spinner 加载中指示器组件封装（基于 lucide Loader2 图标）。
 * 主要组件：Spinner。
 */
import { cn } from "@/lib/utils"
import { Loader2Icon } from "lucide-react"

function Spinner({ className, ...props }: React.ComponentProps<"svg">) {
  return (
    <Loader2Icon data-slot="spinner" role="status" aria-label="Loading" className={cn("size-4 animate-spin", className)} {...props} />
  )
}

export { Spinner }
