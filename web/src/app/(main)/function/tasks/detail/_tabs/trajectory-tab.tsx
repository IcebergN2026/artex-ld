 *   - sortWorkers：agent chip 的固定排序；显示名 workerLabel 复用 transcript 的共享映射。
 *   - Transcript 传 showWorkers：单 lane(如任务只有 planner)时每行仍显示执行者 chip。
import { Transcript, workerColor, workerLabel } from "@/components/transcript";
              <Transcript activity={visible} live={sseLive} taskId={taskId} showWorkers />