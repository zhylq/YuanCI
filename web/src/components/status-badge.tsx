import { cn } from '../lib/cn'
import type { Run } from '../lib/api'

const statusLabel: Record<Run['status'], string> = {
  queued: '排队中', running: '运行中', waiting_approval: '等待审批',
  succeeded: '成功', failed: '失败', canceled: '已取消',
}

const statusClass: Record<Run['status'], string> = {
  queued: 'border-slate-300 bg-slate-50 text-slate-700',
  running: 'border-blue-200 bg-blue-50 text-blue-800',
  waiting_approval: 'border-amber-200 bg-amber-50 text-amber-800',
  succeeded: 'border-emerald-200 bg-emerald-50 text-emerald-800',
  failed: 'border-red-200 bg-red-50 text-red-800',
  canceled: 'border-slate-300 bg-slate-100 text-slate-600',
}

export function StatusBadge({ status }: { status: Run['status'] }) {
  return <span className={cn('inline-flex rounded-full border px-2.5 py-1 text-xs font-medium', statusClass[status])}>{statusLabel[status]}</span>
}
