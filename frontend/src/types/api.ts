export interface ApiResponse<T = unknown> {
  code: number
  message: string
  data: T
  timestamp?: number
}

export type RequestResult<T> =
  | { success: true; data: T; message: string; timestamp?: number }
  | { success: false; message: string; code?: number }

export type ServiceStatus = 'stopped' | 'starting' | 'running' | 'building' | 'snapshotting'

export interface StatusData {
  name: string
  version?: string
  opencode_version?: string
  app_version?: string
  app_remote_version?: string
  app_has_update?: boolean
  target_version?: string
  status: ServiceStatus
  uptime: string
  started_at: number
  server_port?: number
  server_time?: number
  build_time: string
  app_url: string
  pid?: number
  cpu?: string
  memory?: string
  last_message: string
  maintenance_active?: boolean
  config_revision?: string
}

export interface CheckUpdateResult {
  has_update: boolean
  current_version?: string
  remote_version?: string
  opencode_version?: string
  channel?: 'latest' | 'canary'
  message: string
}

export interface RuntimeVersionsResult {
  current_version: string
  versions: string[]
  recommended_version: string
  min_version: string
}

export interface WorkspaceItem {
  workspaceId: string
  path: string
  title: string
  sessionIds: string[]
  sessionsKnown: boolean
  createdAt: string
  updatedAt: string
}

export interface WorkspaceData {
  items: WorkspaceItem[]
}

export interface LogData {
  lines: string[]
  content: string
  sequence: number
}

export interface SettingsConfig {
  revision?: string
  server_port: number
  proxy_port: number
  heap_memory_limit?: number
  network_proxy: string
  proxy_runtime?: boolean
  npm_registry?: string
  update_channel?: 'latest' | 'canary'
  access_mode?: 'fngateway' | 'port' | 'custom'
  reverse_proxy_url: string
  access_password: string
  data_library_path?: string
  version?: string
  build_time?: string
}

export interface WSEnvelope<T = unknown> {
  type: 'status' | 'workspace' | 'snapshot' | 'snapshot_progress' | 'usage' | 'log' | 'log_snapshot' | 'pong' | string
  data: T
  timestamp?: number
}

export interface SnapshotMeta {
  id: string
  name: string
  created_at: number
  size_bytes: number
  compression_level?: number
  app_version?: string
  openchamber_version?: string
  opencode_version?: string
  format_version: number
}

export interface SnapshotProgressTask {
  active: boolean
  can_cancel?: boolean
  action: string
  stage: string
  error?: string
}

export interface SnapshotSummary {
  items: SnapshotMeta[]
  total_size_bytes: number
  disk_free_bytes: number
  disk_total_bytes: number
}

export interface CreateSnapshotParams {
  name: string
  compression_level?: number
}
