<template>
  <div class="w-full flex-1 flex flex-col gap-4 sm:gap-6">
    <!-- 页头与操作区 -->
    <div
      class="sticky -top-[14px] sm:-top-6 z-20 -mt-3.5 sm:-mt-4 pt-5 sm:pt-7 pb-2 sm:pb-2.5 bg-fnos-bg/90 backdrop-blur-md flex items-center justify-between gap-3 w-full transition-all duration-200">
      <h1 class="text-xl font-bold text-slate-800 dark:text-slate-100 tracking-tight">应用设置</h1>

      <!-- 右侧同行操作按钮组 -->
      <div v-auto-animate class="flex items-center gap-2 shrink-0">
        <n-button v-if="isChanged" size="small" :disabled="saving || configLoading" @click="handleReset"
          class="!h-8 !px-3 rounded-lg text-xs font-medium transition-transform duration-150 active:scale-95">
          <template #icon>
            <n-icon :size="15">
              <X />
            </n-icon>
          </template>
          取消
        </n-button>

        <n-button type="primary" size="small" :loading="saving" :disabled="!isChanged || loadError || !configLoaded || remoteChanged || configLoading"
          @click="handleSave"
          class="!h-8 !px-3 rounded-lg text-xs font-medium shadow-xs shadow-fnos-blue/20 transition-all duration-150 active:scale-95 shrink-0">
          保存设置
        </n-button>
      </div>
    </div>

    <!-- 加载状态与表单内容过渡容器 -->
    <div v-auto-animate class="w-full flex-1">
      <!-- 加载失败轻量状态 -->
      <n-card v-if="loadError && !configLoaded" :bordered="false" class="shadow-sm py-12 text-center rounded-2xl">
        <n-empty description="配置加载失败">
          <template #extra>
            <n-button size="small" secondary @click="configStore.fetchConfig(true)"
              class="transition-transform duration-150 active:scale-95">
              重新加载
            </n-button>
          </template>
        </n-empty>
      </n-card>

      <!-- 配置表单内容 -->
      <n-form v-else ref="formRef" :model="config" :rules="rules" :disabled="saving || configLoading || !configLoaded" label-placement="top" size="medium">
        <div class="flex flex-col gap-4 sm:gap-6">
          <n-alert v-if="loadError && configLoaded" type="error" title="读取最新设置失败">
            {{ lastErrorMessage }}，本地修改已保留。
            <n-button size="small" :disabled="saving || configLoading" @click="configStore.fetchConfig()" class="mt-2">
              重试读取
            </n-button>
          </n-alert>
          <n-alert v-if="remoteChanged" type="warning" title="服务端设置已变更">
            本地修改已保留，请重新加载最新设置后再编辑，避免覆盖快照还原或其他客户端的修改。
            <n-button size="small" :disabled="saving || configLoading" @click="configStore.fetchConfig(true)" class="mt-2">
              放弃本地修改并重新加载
            </n-button>
          </n-alert>
          <!-- 核心服务网络与端口配置卡片 -->
          <n-card title="核心服务" :bordered="false" class="shadow-sm rounded-2xl">
            <n-grid :cols="2" :x-gap="20" :y-gap="16" responsive="screen" item-responsive>
              <!-- 第一行：内部端口 反代端口 -->
              <n-gi span="2 m:1">
                <n-form-item label="内部监听端口" path="server_port">
                  <template #label>
                    <div class="flex items-center gap-1.5">
                      <span>内部监听端口</span>
                      <n-tooltip :trigger="isTouch ? 'click' : 'hover'">
                        <template #trigger>
                          <n-icon size="14"
                            class="text-slate-400 dark:text-slate-500 cursor-help transition-colors active:text-fnos-blue dark:active:text-blue-400">
                            <Help />
                          </n-icon>
                        </template>
                        OpenChamber 本地服务监听端口，默认 3000，仅监听本机回环地址
                      </n-tooltip>
                    </div>
                  </template>
                  <n-input-number v-model:value="config.server_port" :min="1" :max="65535" placeholder="3000"
                    class="w-full" />
                </n-form-item>
              </n-gi>

              <n-gi span="2 m:1">
                <n-form-item label="反向代理端口" path="proxy_port">
                  <template #label>
                    <div class="flex items-center gap-1.5">
                      <span>反向代理端口</span>
                      <n-tooltip :trigger="isTouch ? 'click' : 'hover'">
                        <template #trigger>
                          <n-icon size="14"
                            class="text-slate-400 dark:text-slate-500 cursor-help transition-colors active:text-fnos-blue dark:active:text-blue-400">
                            <Help />
                          </n-icon>
                        </template>
                        WebUI 的独立代理端口，默认 3001。未设置访问密码时仅监听 NAS 本机。
                      </n-tooltip>
                    </div>
                  </template>
                  <n-input-number v-model:value="config.proxy_port" :min="1" :max="65535" placeholder="3001"
                    class="w-full" />
                </n-form-item>
              </n-gi>

              <!-- 第二行：堆内存上限 反代密码 -->
              <n-gi span="2 m:1">
                <n-form-item label="堆内存上限" path="heap_memory_limit">
                  <template #label>
                    <div class="flex items-center gap-1.5">
                      <span>堆内存上限 (Heap Memory)</span>
                      <n-tooltip :trigger="isTouch ? 'click' : 'hover'">
                        <template #trigger>
                          <n-icon size="14"
                            class="text-slate-400 dark:text-slate-500 cursor-help transition-colors active:text-fnos-blue dark:active:text-blue-400">
                            <Help />
                          </n-icon>
                        </template>
                        Node.js 核心服务最大堆内存限制 (--max-old-space-size)，若分析超大项目发生内存溢出时可调高。
                      </n-tooltip>
                    </div>
                  </template>
                  <n-select v-model:value="config.heap_memory_limit" :options="heapMemoryOptions"
                    placeholder="自动 (系统默认)" />
                </n-form-item>
              </n-gi>

              <n-gi span="2 m:1">
                <n-form-item label="访问控制密码" path="access_password">
                  <template #label>
                    <div class="flex items-center gap-1.5">
                      <span>反代访问密码</span>
                      <n-tooltip :trigger="isTouch ? 'click' : 'hover'">
                        <template #trigger>
                          <n-icon size="14"
                            class="text-slate-400 dark:text-slate-500 cursor-help transition-colors active:text-fnos-blue dark:active:text-blue-400">
                            <Help />
                          </n-icon>
                        </template>
                        飞牛网关与独立代理端口共用的附加密码。留空时飞牛入口仍需管理员登录，独立代理端口仅允许 NAS 本机连接。
                      </n-tooltip>
                    </div>
                  </template>
                  <n-input type="password" show-password-on="click" v-model:value="config.access_password"
                    placeholder="使用独立端口远程访问时必填" autocomplete="new-password" />
                </n-form-item>
              </n-gi>

              <!-- 第三行：打开方式选择 -->
              <n-gi span="2">
                <n-form-item label="打开方式选择" path="access_mode">
                  <template #label>
                    <div class="flex items-center gap-1.5">
                      <span>打开方式选择</span>
                      <n-tooltip :trigger="isTouch ? 'click' : 'hover'">
                        <template #trigger>
                          <n-icon size="14"
                            class="text-slate-400 dark:text-slate-500 cursor-help transition-colors active:text-fnos-blue dark:active:text-blue-400">
                            <Help />
                          </n-icon>
                        </template>
                        控制概览页「进入 OpenChamber」按钮的访问地址
                      </n-tooltip>
                    </div>
                  </template>
                  <n-radio-group v-model:value="config.access_mode" size="medium" class="w-full flex">
                    <n-radio-button value="fngateway" class="!flex-1 text-center">
                      飞牛网关
                    </n-radio-button>
                    <n-radio-button value="port" class="!flex-1 text-center">
                      反代端口
                    </n-radio-button>
                    <n-radio-button value="custom" class="!flex-1 text-center">
                      自定义地址
                    </n-radio-button>
                  </n-radio-group>
                </n-form-item>
              </n-gi>

              <n-gi v-if="usesGatewayFallback" span="2">
                <n-alert type="warning">
                  未设置访问密码时，概览按钮将使用飞牛网关。要从其他设备访问独立端口，请先设置密码。
                </n-alert>
              </n-gi>

              <!-- 自定义外部地址输入框 (仅在选中自定义地址时动态渲染) -->
              <n-gi v-if="config.access_mode === 'custom'" span="2">
                <n-form-item label="自定义外部访问地址" path="reverse_proxy_url">
                  <template #label>
                    <div class="flex items-center gap-1.5">
                      <span>自定义外部访问地址</span>
                      <n-tooltip :trigger="isTouch ? 'click' : 'hover'">
                        <template #trigger>
                          <n-icon size="14"
                            class="text-slate-400 dark:text-slate-500 cursor-help transition-colors active:text-fnos-blue dark:active:text-blue-400">
                            <Help />
                          </n-icon>
                        </template>
                        点击概览页「进入 OpenChamber」时跳转的完整地址，例如 https://openchamber.example.com
                      </n-tooltip>
                    </div>
                  </template>
                  <n-input v-model:value="config.reverse_proxy_url" placeholder="例如 https://openchamber.example.com"
                    clearable />
                </n-form-item>
              </n-gi>

            </n-grid>
          </n-card>

          <!-- 网络与依赖源卡片 -->
          <n-card title="网络与依赖源" :bordered="false" class="shadow-sm rounded-2xl">
            <div class="space-y-4">
              <n-form-item path="update_channel">
                <template #label>
                  <div class="flex items-center gap-1.5">
                    <span>OpenChamber 版本更新渠道</span>
                    <n-tooltip :trigger="isTouch ? 'click' : 'hover'">
                      <template #trigger>
                        <n-icon size="14"
                          class="text-slate-400 dark:text-slate-500 cursor-help transition-colors active:text-fnos-blue dark:active:text-blue-400">
                          <Help />
                        </n-icon>
                      </template>
                      latest 为稳定版，canary 为预览版，直接对应 OpenChamber 官方 npm dist-tag。仅检查所选渠道，不自动更新或降级。
                    </n-tooltip>
                  </div>
                </template>
                <n-select v-model:value="config.update_channel" :options="updateChannelOptions" />
              </n-form-item>

              <n-form-item path="npm_registry">
                <template #label>
                  <div class="flex items-center gap-1.5">
                    <span>NPM 依赖源</span>
                    <n-tooltip :trigger="isTouch ? 'click' : 'hover'">
                      <template #trigger>
                        <n-icon size="14"
                          class="text-slate-400 dark:text-slate-500 cursor-help transition-colors active:text-fnos-blue dark:active:text-blue-400">
                          <Help />
                        </n-icon>
                      </template>
                      用于在线部署 OpenChamber 与 OpenCode 官方 npm 包
                    </n-tooltip>
                  </div>
                </template>
                <n-select v-model:value="config.npm_registry" :options="npmRegistryOptions" />
              </n-form-item>

              <n-form-item path="network_proxy">
                <template #label>
                  <div class="flex items-center gap-1.5">
                    <span>网络代理地址 (HTTP / HTTPS / SOCKS5)</span>
                    <n-tooltip :trigger="isTouch ? 'click' : 'hover'">
                      <template #trigger>
                        <n-icon size="14"
                          class="text-slate-400 dark:text-slate-500 cursor-help transition-colors active:text-fnos-blue dark:active:text-blue-400">
                          <Help />
                        </n-icon>
                      </template>
                      部署时外部下载使用此代理，所选国内 NPM 镜像源绕过代理；留空直接连接。SOCKS5 仅供 npm 部署使用，服务进程请使用 HTTP 或混合端口。
                    </n-tooltip>
                  </div>
                </template>
                <n-input v-model:value="config.network_proxy"
                  placeholder="例如 http://192.168.0.105:7890" clearable />
              </n-form-item>

              <div class="pt-3 border-t border-slate-100 dark:border-slate-800/80 space-y-1.5">
                <div class="flex items-center justify-between gap-3">
                  <div class="flex items-center gap-1.5">
                    <span class="text-sm font-medium text-slate-800 dark:text-slate-100">应用于服务进程</span>
                    <n-tooltip :trigger="isTouch ? 'click' : 'hover'">
                      <template #trigger>
                        <n-icon size="14"
                          class="text-slate-400 dark:text-slate-500 cursor-help transition-colors active:text-fnos-blue dark:active:text-blue-400">
                          <Help />
                        </n-icon>
                      </template>
                      启用后向 OpenChamber、OpenCode 及其子进程传递 HTTP_PROXY / HTTPS_PROXY 和 npm 代理配置，并启用 Node.js 环境代理。
                    </n-tooltip>
                  </div>
                  <n-switch v-model:value="config.proxy_runtime" :disabled="!config.network_proxy?.trim()"
                    size="medium" class="shrink-0" />
                </div>
                <div class="text-xs text-slate-500 dark:text-slate-400 leading-relaxed">
                  会话内访问 GitHub、Git HTTPS 操作和插件安装需开启此项；保存后会重启运行中的服务。Clash 需允许局域网连接，并开放 HTTP 或混合端口。
                </div>
              </div>
            </div>
          </n-card>

          <!-- 重置修复卡片 -->
          <n-card title="重置修复" :bordered="false"
            class="shadow-sm rounded-2xl border border-rose-100 dark:border-rose-950/40">
            <div class="space-y-2">
              <div class="flex items-center justify-between gap-3">
                <div class="text-sm font-medium text-slate-800 dark:text-slate-100">
                  保留数据并干净部署当前版本
                </div>
                <n-button type="error" size="small" secondary :loading="isBuilding"
                  :disabled="isActionLocked" @click="handleRepairCurrent"
                  class="shrink-0 !h-8 !px-3.5 rounded-xl font-medium transition-transform duration-150 active:scale-95">
                  重置
                </n-button>
              </div>
              <div class="text-xs text-slate-500 dark:text-slate-400 leading-relaxed">
                适用于运行包损坏或服务无法启动等场景。保留持久化配置、模型凭据和会话，在线重新部署当前版本并清理安装临时文件。
              </div>
            </div>
          </n-card>
        </div>
      </n-form>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch, onActivated, onUnmounted } from 'vue'
import { storeToRefs } from 'pinia'
import {
  NCard,
  NAlert,
  NForm,
  NFormItem,
  NGrid,
  NGi,
  NInput,
  NInputNumber,
  NSelect,
  NRadioGroup,
  NRadioButton,
  NEmpty,
  NTooltip,
  NIcon,
  NButton,
  NSwitch,
  useMessage,
  useDialog,
  type FormInst,
  type FormRules
} from 'naive-ui'
import {
  Help,
  X
} from '@vicons/tabler'
import { useConfigStore } from '../stores/config'
import { useSystemStore } from '../stores/system'
import { trimSdk } from '../utils/trimSdk'
import { useIsTouchDevice } from '../utils/device'

const configStore = useConfigStore()
const systemStore = useSystemStore()
const { isBuilding, isActionLocked, isRunning } = storeToRefs(systemStore)
const message = useMessage()
const dialog = useDialog()
const isTouch = useIsTouchDevice()

const formRef = ref<FormInst | null>(null)

const {
  config,
  savedConfig,
  saving,
  loadError,
  lastErrorMessage,
  configLoaded,
  remoteChanged,
  loading: configLoading,
  isChanged,
  isServerPortChanged,
  isHeapMemoryChanged,
  isProxyRuntimeChanged
} = storeToRefs(configStore)

const usesGatewayFallback = computed(() => config.value.access_mode === 'port' && !config.value.access_password?.trim())

// 堆内存上限选项
const heapMemoryOptions = [
  { label: '自动 (系统默认)', value: 0 },
  { label: '2 GB', value: 2 },
  { label: '4 GB', value: 4 },
  { label: '6 GB', value: 6 },
  { label: '8 GB', value: 8 },
  { label: '10 GB', value: 10 }
]

// NPM 依赖源选项
const npmRegistryOptions = [
  { label: '淘宝镜像源 (npmmirror.com)', value: 'https://registry.npmmirror.com' },
  { label: '腾讯云镜像源 (cloud.tencent.com)', value: 'https://mirrors.cloud.tencent.com/npm/' },
  { label: '华为云镜像源 (huaweicloud.com)', value: 'https://repo.huaweicloud.com/repository/npm/' },
  { label: 'npm 官方源 (npmjs.org)', value: 'https://registry.npmjs.org' }
]

const updateChannelOptions = [
  { label: 'latest', value: 'latest' },
  { label: 'canary', value: 'canary' }
]

watch(isChanged, (changed) => {
  if (changed) {
    trimSdk.setExitPageTips({
      title: '设置未保存',
      content: '当前设置有未保存的修改，离开可能丢失这些内容。'
    })
  } else {
    trimSdk.clearExitPageTips()
  }
}, { immediate: true })

onUnmounted(() => {
  trimSdk.clearExitPageTips()
})

// 表单输入校验规则
const rules: FormRules = {
  update_channel: [
    { required: true, enum: ['latest', 'canary'], message: '请选择更新渠道', trigger: 'change' }
  ],
  server_port: [
    { required: true, type: 'number', message: '请输入内部监听端口', trigger: ['input', 'blur'] },
    {
      validator(_rule, value: number) {
        if (!value || value < 1 || value > 65535) {
          return new Error('端口范围必须在 1 ~ 65535 之间')
        }
        if (value === config.value.proxy_port) {
          return new Error('内部监听端口不能与反向代理端口相同')
        }
        return true
      },
      trigger: ['input', 'blur']
    }
  ],
  proxy_port: [
    { required: true, type: 'number', message: '请输入反向代理端口', trigger: ['input', 'blur'] },
    {
      validator(_rule, value: number) {
        if (!value || value < 1 || value > 65535) {
          return new Error('端口范围必须在 1 ~ 65535 之间')
        }
        if (value === config.value.server_port) {
          return new Error('反向代理端口不能与内部监听端口相同')
        }
        return true
      },
      trigger: ['input', 'blur']
    }
  ],
  network_proxy: [
    {
      validator(_rule, value: string) {
        if (!value || !value.trim()) return true
        const trimmed = value.trim()
        if (!/^(http|https|socks5|socks5h):\/\//i.test(trimmed)) {
          return new Error('代理地址需以 http://、https://、socks5:// 或 socks5h:// 开头')
        }
        if (config.value.proxy_runtime && /^socks5h?:/i.test(trimmed)) {
          return new Error('服务进程代理请使用 HTTP 或混合端口，或关闭「应用于服务进程」')
        }
        return true
      },
      trigger: ['input', 'blur']
    }
  ],
  reverse_proxy_url: [
    {
      validator(_rule, value: string) {
        if (config.value.access_mode !== 'custom') return true
        if (!value || !value.trim()) {
          return new Error('请填写自定义外部访问地址')
        }
        const trimmed = value.trim()
        if (!/^(http|https):\/\//i.test(trimmed)) {
          return new Error('外部访问地址需以 http:// 或 https:// 开头')
        }
        return true
      },
      trigger: ['input', 'blur']
    }
  ]
}

onActivated(async () => {
  await configStore.fetchConfig()
  if (loadError.value) message.error('加载配置失败')
})

// 执行实际保存
async function executeSave() {
  const res = await configStore.saveConfig()
  if (res.success) {
    message.success(res.message || '设置保存成功')
  } else {
    message.error(res.message || '保存设置失败')
  }
}

// 提交保存
async function handleSave() {
  if (!formRef.value) return
  try {
    await formRef.value.validate()
  } catch {
    message.error('请检查表单填写是否正确')
    return
  }

  if ((isServerPortChanged.value || isHeapMemoryChanged.value || isProxyRuntimeChanged.value) && isRunning.value) {
    const changes: string[] = []
    if (isServerPortChanged.value) {
      changes.push(`内部监听端口已由 ${savedConfig.value?.server_port || 3000} 变更为 ${config.value.server_port}`)
    }
    if (isHeapMemoryChanged.value) {
      const oldMem = savedConfig.value?.heap_memory_limit ? `${savedConfig.value.heap_memory_limit} GB` : '自动'
      const newMem = config.value.heap_memory_limit ? `${config.value.heap_memory_limit} GB` : '自动'
      changes.push(`堆内存上限已由 ${oldMem} 变更为 ${newMem}`)
    }
    if (isProxyRuntimeChanged.value) {
      const isNowEnabled = Boolean(config.value.network_proxy?.trim()) && Boolean(config.value.proxy_runtime)
      changes.push(`服务进程代理将${isNowEnabled ? '启用或更新' : '关闭'}`)
    }

    dialog.warning({
      title: '确认保存并重启核心服务？',
      content: `检测到${changes.join('，')}。保存后将重启 OpenChamber 及其托管的 OpenCode，正在执行的任务可能中断，是否继续？`,
      positiveText: '保存并重启',
      negativeText: '取消',
      onPositiveClick: async () => {
        await executeSave()
      }
    })
    return
  }

  await executeSave()
}

// 取消修改
function handleReset() {
  configStore.resetConfig()
  formRef.value?.restoreValidation()
  message.info('已取消修改')
}

// 重置修复确认弹窗
function handleRepairCurrent() {
  dialog.warning({
    title: '确认重置修复？',
    content: '将在线重新安装当前版本，校验服务后清理旧包、安装缓存和临时文件。持久化配置、模型凭据和历史会话将保留。是否继续？',
    positiveText: '确认修复',
    negativeText: '取消',
    onPositiveClick: async () => {
      const res = await systemStore.sendAction('repair')
      if (res.success) {
        message.success(res.message || '已开始重置修复…')
      } else {
        message.error(res.message || '重置修复失败')
      }
    }
  })
}
</script>
