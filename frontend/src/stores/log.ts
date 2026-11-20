import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { logApi } from '../api'
import type { RequestResult } from '../types/api'

export const useLogStore = defineStore('log', () => {
  const logLines = ref<string[]>([])
  const logAutoScroll = ref(true)
  const fetching = ref(true)

  const MAX_LOG_LINES = 300
  const FLUSH_INTERVAL = 80

  let pendingBuffer = ''
  let flushTimer: ReturnType<typeof setTimeout> | null = null
  const flushListeners = new Set<() => void>()

  function splitLines(text: string): string[] {
    if (!text) return []
    const raw = text.split('\n')
    const lines: string[] = []
    for (let i = 0; i < raw.length - 1; i++) {
      lines.push(raw[i] + '\n')
    }
    if (raw[raw.length - 1]) {
      lines.push(raw[raw.length - 1])
    }
    return lines
  }

  function trimLogs() {
    if (logLines.value.length > MAX_LOG_LINES) {
      logLines.value = logLines.value.slice(-MAX_LOG_LINES)
    }
  }

  function flushPending() {
    flushTimer = null
    if (!pendingBuffer) return
    const incomingLines = splitLines(pendingBuffer)
    pendingBuffer = ''
    if (incomingLines.length > 0) {
      logLines.value.push(...incomingLines)
      trimLogs()
      flushListeners.forEach((fn) => fn())
    }
  }

  function appendChunk(chunk: string) {
    pendingBuffer += chunk
    if (flushTimer === null) {
      flushTimer = setTimeout(flushPending, FLUSH_INTERVAL)
    }
  }

  function onFlush(fn: () => void): () => void {
    flushListeners.add(fn)
    return () => {
      flushListeners.delete(fn)
    }
  }

  function setLogs(lines: string[]) {
    pendingBuffer = ''
    if (flushTimer !== null) clearTimeout(flushTimer)
    flushTimer = null
    fetching.value = false
    const flattened: string[] = []
    for (const l of lines) {
      flattened.push(...splitLines(l))
    }
    logLines.value = flattened.slice(-MAX_LOG_LINES)
    flushListeners.forEach((fn) => fn())
  }

  const displayedText = computed(() => logLines.value.join(''))

  async function clearLogs(): Promise<RequestResult<boolean>> {
    return logApi.clearLogs()
  }

  function downloadLogs(): void {
    window.open(logApi.getDownloadUrl(), '_blank')
  }

  return {
    logLines,
    logAutoScroll,
    fetching,
    displayedText,
    appendChunk,
    setLogs,
    clearLogs,
    downloadLogs,
    onFlush
  }
})
