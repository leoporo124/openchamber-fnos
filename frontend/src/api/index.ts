import { http } from '../utils/http'
import type {
  StatusData,
  CheckUpdateResult,
  RuntimeVersionsResult,
  SettingsConfig,
  CreateSnapshotParams
} from '../types/api'

/**
 * 系统与进程控制 API
 */
export const systemApi = {
  getStatus: () => http.get<StatusData>('status'),
  checkUpdate: () => http.get<CheckUpdateResult>('check-update', { timeout: 45000 }),
  getRuntimeVersions: () => http.get<RuntimeVersionsResult>('deploy/versions'),
  sendAction: (action: 'start' | 'stop' | 'restart' | 'upgrade' | 'deploy' | 'repair' | string, payload?: Record<string, unknown>) =>
    http.post<StatusData>('action', { action, ...payload })
}

/**
 * 日志流与文件 API
 */
export const logApi = {
  clearLogs: () => http.delete<boolean>('logs'),
  getDownloadUrl: () => 'api/logs/download'
}

/**
 * 应用配置 API
 */
export const configApi = {
  getConfig: () => http.get<SettingsConfig>('config'),
  saveConfig: (config: SettingsConfig, revision: string) =>
    http.post<SettingsConfig>('config', config, { headers: { 'If-Match': JSON.stringify(revision) } })
}

/**
 * 快照管理 API
 */
export const snapshotApi = {
  create: (params: CreateSnapshotParams) =>
    http.post<{ name: string }>('snapshots', params),
  cancel: () => http.post('snapshots/cancel'),
  restore: (id: string) =>
    http.post<{ id: string; name: string }>(`snapshots/${encodeURIComponent(id)}/restore`),
  delete: (id: string) => http.delete(`snapshots/${encodeURIComponent(id)}`)
}
