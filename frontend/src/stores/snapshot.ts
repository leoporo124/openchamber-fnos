import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import type { SnapshotMeta, SnapshotSummary, SnapshotProgressTask, CreateSnapshotParams } from '../types/api'
import { snapshotApi } from '../api'

export const useSnapshotStore = defineStore('snapshot', () => {
  const snapshots = ref<SnapshotMeta[]>([])
  const totalSizeBytes = ref(0)
  const loading = ref(true)
  const listError = ref('')
  const submitting = ref(false)
  const cancelLoading = ref(false)
  const task = ref<SnapshotProgressTask | null>(null)
  const actionLoading = computed(() => submitting.value || Boolean(task.value?.active))

  function updateProgress(data: SnapshotProgressTask) {
    task.value = data?.action ? data : null
  }

  function setListError(message: string) {
    listError.value = message
    loading.value = false
  }

  function updateSummary(summary: SnapshotSummary) {
    if (!summary) return
    snapshots.value = summary.items || []
    totalSizeBytes.value = summary.total_size_bytes || 0
    loading.value = false
    listError.value = ''
  }

  async function submit<T>(request: () => Promise<T>) {
    submitting.value = true
    try {
      return await request()
    } finally {
      submitting.value = false
    }
  }

  function createSnapshot(params: CreateSnapshotParams) {
    return submit(() => snapshotApi.create(params))
  }

  function restoreSnapshot(id: string) {
    return submit(() => snapshotApi.restore(id))
  }

  function deleteSnapshot(id: string) {
    return submit(() => snapshotApi.delete(id))
  }

  async function cancelCreateSnapshot() {
    cancelLoading.value = true
    try {
      return await snapshotApi.cancel()
    } finally {
      cancelLoading.value = false
    }
  }

  return {
    snapshots,
    totalSizeBytes,
    loading,
    listError,
    setListError,
    actionLoading,
    cancelLoading,
    task,
    updateProgress,
    updateSummary,
    createSnapshot,
    cancelCreateSnapshot,
    restoreSnapshot,
    deleteSnapshot
  }
})
