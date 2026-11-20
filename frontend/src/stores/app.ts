import { defineStore } from 'pinia'
import { ref } from 'vue'
import { wsClient } from '../utils/websocket'
import { useSystemStore } from './system'
import { useWorkspaceStore } from './workspace'
import { useLogStore } from './log'
import { useSnapshotStore } from './snapshot'
import { useConfigStore } from './config'

export const useAppStore = defineStore('app', () => {
  const currentTab = ref('overview')
  let isInitialized = false

  function setTab(tab: string) {
    currentTab.value = tab
  }

  function init() {
    if (isInitialized) return
    isInitialized = true

    const systemStore = useSystemStore()
    const workspaceStore = useWorkspaceStore()
    const logStore = useLogStore()
    const snapshotStore = useSnapshotStore()
    const configStore = useConfigStore()

    systemStore.startClock()

    // 绑定 WebSocket 事件分发
    wsClient.on('connectionChange', (connected) => {
      systemStore.setWsConnected(connected)
    })

    wsClient.on('status', (data) => {
      systemStore.updateStatus(data)
      configStore.checkRevision(data.config_revision)
    })

    wsClient.on('workspace', (data) => {
      workspaceStore.updateWorkspaceData(data)
    })

    wsClient.on('snapshot', (data) => {
      snapshotStore.updateSummary(data)
    })

    wsClient.on('snapshot_progress', (data) => {
      snapshotStore.updateProgress(data)
    })

    wsClient.on('usage', (data) => {
      systemStore.updateUsage(data.cpu, data.memory)
    })

    wsClient.on('log', (chunk) => {
      logStore.appendChunk(chunk)
    })

    wsClient.on('log_snapshot', (data) => {
      logStore.setLogs(data.lines || [])
    })

    wsClient.on('reconnected', () => {
      if (configStore.configLoaded) void configStore.fetchConfig()
    })

    // 建立连接
    systemStore.setWsConnected(false)
    wsClient.connect()
  }

  return {
    currentTab,
    setTab,
    init
  }
})
