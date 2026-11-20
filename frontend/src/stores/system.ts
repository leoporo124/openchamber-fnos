import { defineStore } from 'pinia'
import { ref, computed, onScopeDispose } from 'vue'
import type { StatusData, CheckUpdateResult, RuntimeVersionsResult, RequestResult } from '../types/api'
import { systemApi, configApi } from '../api'
import { trimSdk } from '../utils/trimSdk'

export const useSystemStore = defineStore('system', () => {
  const statusData = ref<StatusData>({
    name: 'OpenChamber',
    version: '-',
    status: 'stopped',
    uptime: '-',
    started_at: 0,
    build_time: '-',
    app_url: '/app/openchamber/',
    cpu: '-',
    memory: '-',
    last_message: ''
  })

  const wsUnavailable = ref(false)
  const serverTimeOffset = ref(0)
  const activeAction = ref<string | null>(null)
  const isCheckingUpdate = ref(false)
  const isCheckingVersions = ref(false)
  const currentTime = ref(Date.now())
  const statusLoaded = ref(false)

  let clockTimer: ReturnType<typeof setInterval> | null = null
  let actionTimeoutTimer: ReturnType<typeof setTimeout> | null = null
  let statusTimeoutTimer: ReturnType<typeof setTimeout> | null = null
  let wsWarningTimer: ReturnType<typeof setTimeout> | null = null
  const wsWarningDelay = 3000

  // 启动 3 秒超时保底：超过 3 秒若未收到状态也强制放行渲染
  function startStatusTimeout() {
    if (statusLoaded.value) return
    if (!statusTimeoutTimer) {
      statusTimeoutTimer = setTimeout(() => {
        statusLoaded.value = true
      }, 3000)
    }
  }

  function clearActionLock() {
    activeAction.value = null
    if (actionTimeoutTimer) {
      clearTimeout(actionTimeoutTimer)
      actionTimeoutTimer = null
    }
  }

  function startClock() {
    if (clockTimer) return
    clockTimer = setInterval(() => {
      currentTime.value = Date.now()
    }, 1000)
  }

  function stopClock() {
    if (clockTimer) {
      clearInterval(clockTimer)
      clockTimer = null
    }
  }

  onScopeDispose(() => {
    stopClock()
    if (actionTimeoutTimer) clearTimeout(actionTimeoutTimer)
    if (statusTimeoutTimer) clearTimeout(statusTimeoutTimer)
    if (wsWarningTimer !== null) clearTimeout(wsWarningTimer)
  })

  function setWsConnected(connected: boolean) {
    if (wsWarningTimer !== null) {
      clearTimeout(wsWarningTimer)
      wsWarningTimer = null
    }
    if (connected) {
      wsUnavailable.value = false
      return
    }
    if (wsUnavailable.value) return
    wsWarningTimer = setTimeout(() => {
      wsWarningTimer = null
      wsUnavailable.value = true
    }, wsWarningDelay)
  }

  function updateStatus(data: Partial<StatusData>) {
    statusLoaded.value = true
    if (statusTimeoutTimer) {
      clearTimeout(statusTimeoutTimer)
      statusTimeoutTimer = null
    }

    const oldStatus = statusData.value.status
    statusData.value = {
      ...statusData.value,
      ...data
    }
    if (data.server_time) {
      serverTimeOffset.value = data.server_time * 1000 - Date.now()
    }
    const curStatus = statusData.value.status

    // 状态流转完成时自动解除动作锁定
    if (activeAction.value) {
      const isTransitionToRunning = oldStatus !== 'running' && curStatus === 'running'
      const isFailedOrStopped = (oldStatus === 'starting' || oldStatus === 'building') && curStatus === 'stopped'

      if (activeAction.value === 'stop') {
        if (curStatus === 'stopped') {
          clearActionLock()
        }
      } else if (activeAction.value === 'start' || activeAction.value === 'restart') {
        if (isTransitionToRunning || isFailedOrStopped) {
          clearActionLock()
        }
      } else if (
        activeAction.value === 'upgrade' ||
        activeAction.value === 'deploy' ||
        activeAction.value === 'repair'
      ) {
        if (!statusData.value.maintenance_active && (curStatus === 'running' || curStatus === 'stopped')) {
          clearActionLock()
        }
      }
    }
  }

  const isRunning = computed(() => statusData.value.status === 'running')
  const isStarting = computed(() => statusData.value.status === 'starting')
  const isBuilding = computed(() => statusData.value.status === 'building')
  const isSnapshotting = computed(() => statusData.value.status === 'snapshotting')
  const isActionLocked = computed(() => Boolean(activeAction.value) || Boolean(statusData.value.maintenance_active) || isBuilding.value || isStarting.value || isSnapshotting.value)

  const statusTagType = computed<'success' | 'warning' | 'info' | 'default'>(() => {
    if (statusData.value.maintenance_active) return 'info'
    if (isRunning.value) return 'success'
    if (isStarting.value) return 'warning'
    if (isBuilding.value || isSnapshotting.value) return 'info'
    return 'default'
  })

  const statusLabel = computed(() => {
    if (statusData.value.maintenance_active) return '更新维护中'
    if (isRunning.value) return '运行中'
    if (isStarting.value) return '启动中'
    if (isBuilding.value) {
      if (activeAction.value === 'upgrade' || statusData.value.last_message?.includes('更新')) {
        return '更新中'
      }
      return '部署中'
    }
    if (isSnapshotting.value) return '快照中'
    return '已停止'
  })

  function formatDuration(total: number): string {
    const h = Math.floor(total / 3600)
    const m = Math.floor((total % 3600) / 60)
    const s = total % 60
    if (h > 0) return `${h}小时${m}分${s}秒`
    if (m > 0) return `${m}分${s}秒`
    return `${s}秒`
  }

  const uptimeText = computed(() => {
    const s = statusData.value
    if (s.status !== 'running' || !s.started_at) return '-'
    const serverNow = currentTime.value + serverTimeOffset.value
    const secs = Math.max(0, Math.floor((serverNow - s.started_at * 1000) / 1000))
    return formatDuration(secs)
  })

  async function sendAction(action: string, payload?: Record<string, unknown>): Promise<RequestResult<StatusData>> {
    if (isActionLocked.value) {
      return { success: false, message: '当前有任务正在进行中，请稍候' }
    }

    activeAction.value = action

    // 超时兜底（最多锁定 60s）
    actionTimeoutTimer = setTimeout(() => {
      clearActionLock()
    }, 60000)

    try {
      const res = await systemApi.sendAction(action, payload)
      if (res.success) updateStatus(res.data)
      if (!res.success) {
        clearActionLock()
      }
      return res
    } catch {
      clearActionLock()
    }
    return { success: false, message: '请求失败' }
  }

  async function checkUpdate(): Promise<RequestResult<CheckUpdateResult>> {
    if (isCheckingUpdate.value) {
      return { success: false, message: '正在检查更新中，请稍候' }
    }
    isCheckingUpdate.value = true
    try {
      return await systemApi.checkUpdate()
    } finally {
      isCheckingUpdate.value = false
    }
  }

  async function fetchRuntimeVersions(): Promise<RequestResult<RuntimeVersionsResult>> {
    if (isCheckingVersions.value) {
      return { success: false, message: '正在获取版本列表，请稍候' }
    }
    isCheckingVersions.value = true
    try {
      return await systemApi.getRuntimeVersions()
    } finally {
      isCheckingVersions.value = false
    }
  }

  async function openWorkApp(): Promise<void> {
    const res = await configApi.getConfig()
    const cfg = res.success ? res.data : null
    const mode = cfg?.access_mode || (cfg?.reverse_proxy_url ? 'custom' : 'fngateway')

    if (mode === 'custom' && cfg?.reverse_proxy_url) {
      await trimSdk.openURL(cfg.reverse_proxy_url, '_blank')
      return
    }

    if (mode === 'port') {
      const port = cfg?.proxy_port || 3001
      await trimSdk.openURL(`http://${window.location.hostname}:${port}/`, '_blank')
      return
    }

    // 默认飞牛网关直连模式
    const gatewayUrl = `${window.location.origin}/app/openchamber/fngateway/`
    await trimSdk.openURL(gatewayUrl, '_blank')
  }

  // 首屏快速拉取状态
  async function fetchInitialStatus(): Promise<void> {
    if (statusLoaded.value) return
    startStatusTimeout()
    try {
      const res = await systemApi.getStatus()
      if (res.success && res.data) {
        updateStatus(res.data)
      }
    } catch {
      // 捕获异常，依赖 WebSocket 或 3 秒超时保底
    }
  }

  const serviceCpu = computed(() => statusData.value.cpu || '-')
  const serviceMemory = computed(() => statusData.value.memory || '-')

  function updateUsage(cpu?: string, memory?: string) {
    if (cpu !== undefined) statusData.value.cpu = cpu
    if (memory !== undefined) statusData.value.memory = memory
  }

  return {
    statusData,
    statusLoaded,
    wsUnavailable,
    serverTimeOffset,
    activeAction,
    isCheckingUpdate,
    isCheckingVersions,
    isActionLocked,
    isRunning,
    isStarting,
    isBuilding,
    isSnapshotting,
    statusTagType,
    statusLabel,
    uptimeText,
    serviceCpu,
    serviceMemory,
    updateUsage,
    startClock,
    setWsConnected,
    updateStatus,
    fetchInitialStatus,
    sendAction,
    checkUpdate,
    fetchRuntimeVersions,
    openWorkApp
  }
})
