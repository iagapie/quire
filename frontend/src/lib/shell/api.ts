// The API of MongoDB and DocumentDB connections, under the names the shell components
// use. Connections themselves (connect, databases, profiles) go through ../api.
export { Shell, Transfers } from '../../../bindings/github.com/iagapie/quire/internal/docapp'
export type {
  CollectionStat,
  ExportRequest,
  FilesDropped,
  ImportFile,
  ImportRequest,
  ImportScan,
  IndexDetails,
  IndexSaveResult,
  TransferItem,
  TransferJob,
} from '../../../bindings/github.com/iagapie/quire/internal/docapp'
export type { Collection, Index } from '../../../bindings/github.com/iagapie/quire/internal/docsession'
export type { Query, Page, Result, RunRequest, RunResult } from '../../../bindings/github.com/iagapie/quire/internal/shell'
export { Mode } from '../../../bindings/github.com/iagapie/quire/internal/shell'
export { Files, errorMessage } from '../api'
