import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import type { SnapshotMeta, SnapshotSummary, SnapshotProgressTask, CreateSnapshotParams } from '../types/api'
import { snapshotApi } from '../api'

export const useSnapshotStore = defineStore('snapshot', () => {
  const snapshots = ref<SnapshotMeta[]>([])
  const totalSizeBytes = ref(0)
  const loading = ref(true)
  const actionLoading = ref(false)
  const cancelLoading = ref(false)

  // 页面切换时保留当前任务进度。
  const progressVisible = ref(false)
  const progressPercent = ref(0)
  const progressStage = ref('')
  const progressMessage = ref('')
  const progressAction = ref<'create' | 'restore' | ''>('')

  let hideTimer: ReturnType<typeof setTimeout> | null = null

  const progressTitle = computed(() => {
    if (progressMessage.value && progressStage.value) {
      return `${progressStage.value} · ${progressMessage.value}`
    }
    if (progressStage.value) {
      return progressStage.value
    }
    return progressAction.value === 'restore' ? '正在还原快照' : '正在处理快照'
  })

  function updateProgress(data: SnapshotProgressTask) {
    if (!data) return
    progressAction.value = data.action === 'restore' || data.action === 'create' ? data.action : ''
    progressPercent.value = Math.max(0, Math.min(100, data.percent))
    progressStage.value = data.stage || ''
    progressMessage.value = data.error || data.message || ''
    actionLoading.value = Boolean(data.active)
    if (data.active) {
      clearHideTimer()
      progressVisible.value = true
      return
    }
    cancelLoading.value = false
    if (!data.action) {
      clearHideTimer()
      progressVisible.value = false
      return
    }
    if (progressVisible.value && !hideTimer) {
      hideTimer = setTimeout(() => {
        progressVisible.value = false
        hideTimer = null
      }, data.error ? 3000 : 1500)
    }
  }

  function clearHideTimer() {
    if (hideTimer) clearTimeout(hideTimer)
    hideTimer = null
  }

  function updateSummary(summary: SnapshotSummary) {
    if (!summary) return
    snapshots.value = summary.items || []
    totalSizeBytes.value = summary.total_size_bytes || 0
    loading.value = false

    if (summary.current_task) updateProgress(summary.current_task)
  }

  async function createSnapshot(params: CreateSnapshotParams) {
    clearHideTimer()
    actionLoading.value = true
    progressVisible.value = true
    progressPercent.value = 0
    progressAction.value = 'create'
    progressStage.value = `正在启动快照创建任务「${params.name}」`
    progressMessage.value = '准备执行环境'

    try {
      const res = await snapshotApi.create(params)
      if (!res.success) {
        progressVisible.value = false
        actionLoading.value = false
      }
      return res
    } catch (err) {
      progressVisible.value = false
      actionLoading.value = false
      throw err
    }
  }

  async function restoreSnapshot(id: string, name?: string) {
    clearHideTimer()
    actionLoading.value = true
    progressVisible.value = true
    progressPercent.value = 5
    progressAction.value = 'restore'
    progressStage.value = `正在启动快照还原任务「${name || id}」`
    progressMessage.value = '校验并准备还原'

    try {
      const res = await snapshotApi.restore(id)
      if (!res.success) {
        progressVisible.value = false
        actionLoading.value = false
      }
      return res
    } catch (err) {
      progressVisible.value = false
      actionLoading.value = false
      throw err
    }
  }

  async function deleteSnapshot(id: string) {
    actionLoading.value = true
    try {
      return await snapshotApi.delete(id)
    } finally {
      actionLoading.value = false
    }
  }

  async function cancelCreateSnapshot() {
    cancelLoading.value = true
    try {
      const res = await snapshotApi.cancel()
      return res
    } finally {
      cancelLoading.value = false
    }
  }

  return {
    snapshots,
    totalSizeBytes,
    loading,
    actionLoading,
    cancelLoading,
    progressVisible,
    progressPercent,
    progressStage,
    progressMessage,
    progressAction,
    progressTitle,
    updateProgress,
    updateSummary,
    createSnapshot,
    cancelCreateSnapshot,
    restoreSnapshot,
    deleteSnapshot
  }
})
