import type { RequestOutcome, RequestRecord } from "../api";
import { formatMilliseconds, formatNumber, formatTimestamp, formatUSD } from "../lib/format";

const COMPACT_LIMIT = 5;

const OUTCOME_LABEL: Record<RequestOutcome, string> = {
  success: "成功",
  error: "失败",
  cancelled: "中断",
  incomplete: "未完成",
};

const OUTCOME_CLASS: Record<RequestOutcome, string> = {
  success: "status status-success",
  error: "status status-error",
  cancelled: "status status-cancelled",
  incomplete: "status status-incomplete",
};

function outcomeOf(record: RequestRecord): RequestOutcome {
  return OUTCOME_LABEL[record.outcome] ? record.outcome : "incomplete";
}

function callerLabel(record: RequestRecord): string {
  const caller = record.caller_id.trim();
  return caller === "" ? "未认证" : caller;
}

function usageLabel(record: RequestRecord): string {
  const input = formatNumber(record.input_tokens);
  const output = formatNumber(record.output_tokens);
  if (input === "\u2014" && output === "\u2014") return "\u2014";
  return `${input} / ${output}`;
}

export interface RequestTableProps {
  records: RequestRecord[];
  compact?: boolean;
}

export function RequestTable({ records, compact = false }: RequestTableProps) {
  const rows = compact ? records.slice(0, COMPACT_LIMIT) : records;

  if (rows.length === 0) {
    return <p className="empty-state">暂无调用记录</p>;
  }

  return (
    <div className="table-scroll">
      <table className="request-table">
        <thead>
          <tr>
            <th scope="col">时间</th>
            <th scope="col">调用方</th>
            <th scope="col">模型</th>
            <th scope="col">状态</th>
            <th scope="col">输入/输出Tokens</th>
            <th scope="col">耗时</th>
            {!compact && <th scope="col">请求ID</th>}
            {!compact && <th scope="col">入口</th>}
            {!compact && <th scope="col">实际上游</th>}
            {!compact && <th scope="col">首次输出</th>}
            {!compact && <th scope="col">估算费用</th>}
          </tr>
        </thead>
        <tbody>
          {rows.map((record) => {
            const outcome = outcomeOf(record);
            return (
              <tr key={record.id}>
                <td className="mono">{formatTimestamp(record.started_at)}</td>
                <td>{callerLabel(record)}</td>
                <td>{record.model || "\u2014"}</td>
                <td className={OUTCOME_CLASS[outcome]}>
                  {OUTCOME_LABEL[outcome]}
                  <span className="muted mono"> {record.status}</span>
                </td>
                <td className="mono">{usageLabel(record)}</td>
                <td className="mono">{formatMilliseconds(record.duration_ms)}</td>
                {!compact && <td className="mono">{record.id}</td>}
                {!compact && <td>{record.entry || "\u2014"}</td>}
                {!compact && (
                  <td>
                    {record.upstream_model || "\u2014"}
                    {record.provider_id ? (
                      <span className="muted"> · {record.provider_id}</span>
                    ) : null}
                  </td>
                )}
                {!compact && <td className="mono">{formatMilliseconds(record.ttft_ms)}</td>}
                {!compact && <td className="mono">{formatUSD(record.estimated_cost)}</td>}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
