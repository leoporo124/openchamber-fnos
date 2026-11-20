import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { configApi } from '../api'
import type { SettingsConfig, RequestResult } from '../types/api'

export const useConfigStore = defineStore('config', () => {
  const config = ref<SettingsConfig>({
    server_port: 3000,
    proxy_port: 3001,
    heap_memory_limit: 0,
    network_proxy: '',
    proxy_runtime: false,
    npm_registry: 'https://registry.npmmirror.com',
    update_channel: 'latest',
    access_mode: 'fngateway',
    reverse_proxy_url: '',
    access_password: ''
  })

  const savedConfig = ref<SettingsConfig | null>(null)
  const loading = ref(false)
  const saving = ref(false)
  const loadError = ref(false)
  const lastErrorMessage = ref('')
  const configLoaded = ref(false)
  const remoteChanged = ref(false)

  // 检测是否存在未保存的配置更改
  const isChanged = computed(() => {
    if (!savedConfig.value) return false
    return (
      config.value.server_port !== savedConfig.value.server_port ||
      config.value.proxy_port !== savedConfig.value.proxy_port ||
      (config.value.heap_memory_limit ?? 0) !== (savedConfig.value.heap_memory_limit ?? 0) ||
      (config.value.access_mode || 'fngateway') !== (savedConfig.value.access_mode || 'fngateway') ||
      (config.value.network_proxy || '') !== (savedConfig.value.network_proxy || '') ||
      Boolean(config.value.proxy_runtime) !== Boolean(savedConfig.value.proxy_runtime) ||
      (config.value.npm_registry || 'https://registry.npmmirror.com') !== (savedConfig.value.npm_registry || 'https://registry.npmmirror.com') ||
      (config.value.update_channel || 'latest') !== (savedConfig.value.update_channel || 'latest') ||
      (config.value.reverse_proxy_url || '') !== (savedConfig.value.reverse_proxy_url || '') ||
      (config.value.access_password || '') !== (savedConfig.value.access_password || '')
    )
  })

  // 内部监听端口是否变更
  const isServerPortChanged = computed(() => {
    if (!savedConfig.value) return false
    return config.value.server_port !== savedConfig.value.server_port
  })

  // 堆内存上限是否变更
  const isHeapMemoryChanged = computed(() => {
    if (!savedConfig.value) return false
    return (config.value.heap_memory_limit ?? 0) !== (savedConfig.value.heap_memory_limit ?? 0)
  })

  // 运行时网络代理变更需要重启服务。
  const isProxyRuntimeChanged = computed(() => {
    if (!savedConfig.value) return false
    const oldEffective = Boolean(savedConfig.value.network_proxy) && Boolean(savedConfig.value.proxy_runtime)
    const newEffective = Boolean(config.value.network_proxy) && Boolean(config.value.proxy_runtime)
    return oldEffective !== newEffective || (newEffective && (
      config.value.network_proxy !== savedConfig.value.network_proxy ||
      config.value.npm_registry !== savedConfig.value.npm_registry
    ))
  })

  // 放弃修改，还原为当前已保存的服务器配置
  function resetConfig() {
    if (savedConfig.value) {
      config.value = { ...savedConfig.value }
    }
  }

  // 加载服务端配置
  async function fetchConfig(discardChanges = false): Promise<void> {
    if (loading.value || saving.value) return
    const draft = JSON.stringify(config.value)
    loading.value = true
    loadError.value = false
    lastErrorMessage.value = ''
    try {
      const res = await configApi.getConfig()
      if (res.success && res.data) {
        const data = { ...res.data }
        if (!data.access_mode) {
          data.access_mode = data.reverse_proxy_url ? 'custom' : 'fngateway'
        }
        data.heap_memory_limit = data.heap_memory_limit ?? 0
        if (data.proxy_runtime === undefined) {
          data.proxy_runtime = false
        }
        if (!data.npm_registry) {
          data.npm_registry = 'https://registry.npmmirror.com'
        }
        data.update_channel = data.update_channel || 'latest'
        if (JSON.stringify(config.value) !== draft || (!discardChanges && isChanged.value)) {
          remoteChanged.value = savedConfig.value?.revision !== data.revision
          return
        }
        config.value = { ...data }
        savedConfig.value = { ...data }
        configLoaded.value = true
        remoteChanged.value = false
      } else {
        loadError.value = true
        lastErrorMessage.value = res.message || '加载配置失败'
      }
    } catch (e: any) {
      loadError.value = true
      lastErrorMessage.value = e?.message || '加载配置失败'
    } finally {
      loading.value = false
    }
  }

  function checkRevision(revision?: string) {
    if (!configLoaded.value || !revision || revision === savedConfig.value?.revision || saving.value) return
    remoteChanged.value = true
    if (!isChanged.value) void fetchConfig()
  }

  // 手动保存配置
  async function saveConfig(): Promise<RequestResult<SettingsConfig>> {
    if (saving.value || loading.value) {
      return { success: false, message: '正在读取或保存设置，请稍候' }
    }
    if (remoteChanged.value || !savedConfig.value?.revision) {
      return { success: false, message: '请重新加载服务端设置后再保存' }
    }
    saving.value = true
    try {
      if (!config.value.network_proxy?.trim()) {
        config.value.proxy_runtime = false
      }
      const res = await configApi.saveConfig(config.value, savedConfig.value.revision)
      if (res.success && res.data) {
        config.value = { ...res.data }
        savedConfig.value = { ...res.data }
        remoteChanged.value = false
      } else if (!res.success && res.code === 412) {
        remoteChanged.value = true
      }
      return res
    } finally {
      saving.value = false
    }
  }

  return {
    config,
    savedConfig,
    loading,
    saving,
    loadError,
    lastErrorMessage,
    configLoaded,
    remoteChanged,
    isChanged,
    isServerPortChanged,
    isHeapMemoryChanged,
    isProxyRuntimeChanged,
    fetchConfig,
    checkRevision,
    saveConfig,
    resetConfig
  }
})
