/**
 * page.tsx：站点落地页，直接重定向到任务页 /function/tasks。
 * 主要函数/类型：
 *   - Home：执行 redirect 的重定向入口
 */
import { redirect } from "next/navigation";

export default function Home() {
  redirect("/function/tasks");
  return <>Coming Soon</>;
}
