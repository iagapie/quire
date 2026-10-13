export { Connections, Files, Preferences, Queries, Transfers } from '../../bindings/github.com/iagapie/quire/internal/app'
export type {
  CompletionInfo,
  FilesDropped,
  HealthEvent,
  ImportFile,
  ImportScan,
  IndexOptions,
  IndexPlan,
  RetryEvent,
  ScriptFile,
  SessionInfo,
  TableDetails,
  TestResult,
  TransferItem,
  TransferJob,
} from '../../bindings/github.com/iagapie/quire/internal/app'
export type { Profile, SSH, TLS } from '../../bindings/github.com/iagapie/quire/internal/profile'
export type {
  Choice,
  Column as TableColumn,
  Constraint,
  Field,
  Index,
  IndexColumn,
  IndexFeatures,
  Info as DriverInfo,
  $Object as DbObject,
  Snippet,
  TableStat,
} from '../../bindings/github.com/iagapie/quire/internal/dialect'
export type { Column, Edit, EditRequest, EditStatement, Page, Result, RunRequest, RunResult, TableInfo, TxState } from '../../bindings/github.com/iagapie/quire/internal/query'
export { Mode } from '../../bindings/github.com/iagapie/quire/internal/query'

export function errorMessage(err: unknown): string {
  if (err instanceof Error) return err.message
  if (typeof err === 'string') return err
  if (err && typeof err === 'object' && 'message' in err) return String((err as { message: unknown }).message)
  return String(err)
}
