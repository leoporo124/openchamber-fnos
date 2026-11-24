<template>
  <div class="w-full flex-1 flex flex-col gap-3.5 sm:gap-6 pb-20 sm:pb-6">
    <!-- 页头：桌面与移动端响应式布局 -->
    <div
      class="sticky -top-[14px] sm:-top-6 z-20 -mt-3.5 sm:-mt-4 pt-5 sm:pt-7 pb-2.5 bg-fnos-bg/90 backdrop-blur-md flex items-center justify-between gap-2.5">
      <div class="flex items-baseline gap-2 min-w-0">
        <h1 class="text-lg sm:text-xl font-bold text-slate-800 dark:text-slate-100 tracking-tight shrink-0">快照管理</h1>
        <span v-if="snapshots.length" class="text-xs text-slate-400 dark:text-slate-500 font-medium truncate"
          :title="`共 ${snapshots.length} 个 · ${formatSize(totalSizeBytes)}`">
          共 {{ snapshots.length }} 个 · {{ formatSize(totalSizeBytes) }}
        </span>
      </div>

      <div class="flex items-center gap-1.5 sm:gap-2 shrink-0">
        <n-button type="primary" size="small" :disabled="actionsDisabled" @click="openCreateModal"
          class="!px-3 sm:!h-9 rounded-lg">
          创建快照
        </n-button>
      </div>
    </div>

    <div v-if="task || wsUnavailable || listError" class="space-y-2">
      <div v-if="task" class="flex items-start justify-between gap-3 rounded-xl bg-fnos-card p-3 text-sm">
        <div role="status" class="min-w-0">
          <p class="font-medium">{{ task.stage }}</p>
          <p v-if="task.error" class="mt-1 break-words text-red-700 dark:text-red-400 select-text">{{ task.error }}</p>
        </div>
        <n-button v-if="task.active && task.action === 'create'" size="small" secondary type="error" class="shrink-0"
          :disabled="!task.can_cancel || cancelLoading || wsUnavailable" @click="promptCancelCreate">
          <template #icon><n-icon><X /></n-icon></template>
          取消
        </n-button>
      </div>
      <n-alert v-if="wsUnavailable" type="warning">实时连接暂不可用，正在自动重试。</n-alert>
      <n-alert v-if="listError" type="error">{{ listError }}</n-alert>
    </div>

    <!-- 快照列表 / 空状态 -->
    <div class="w-full flex-1">
      <p v-if="loading && !wsUnavailable" role="status" class="py-6 text-center text-sm">正在读取快照…</p>
      <n-card v-else-if="!snapshots.length && !loading && !listError" :bordered="false" class="py-12 text-center shadow-sm rounded-2xl">
        <n-empty description="暂无快照备份">
          <template #icon>
            <n-icon :size="48" class="text-slate-300 dark:text-slate-600">
              <History />
            </n-icon>
          </template>
        </n-empty>
      </n-card>

      <!-- 单列快照卡片流（一行一个，融合工作区卡片设计风格） -->
      <div v-else class="flex flex-col gap-2.5 sm:gap-3">
        <n-card v-for="item in snapshots" :key="item.id" hoverable :bordered="false"
          class="select-none shadow-sm group rounded-2xl"
          content-style="padding: 12px 14px;">
          <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3 sm:gap-4">
            <!-- 左侧核心信息 -->
            <div class="flex items-start sm:items-center gap-2.5 sm:gap-3.5 min-w-0 flex-1">
              <!-- 时光机图标：工作区同款层次感底色与 hover 缩放高亮 -->
              <div
                class="w-10 h-10 rounded-xl bg-slate-100 dark:bg-white/[0.06] text-slate-500 dark:text-slate-400 flex items-center justify-center shrink-0 mt-0.5 sm:mt-0">
                <n-icon :size="20">
                  <History />
                </n-icon>
              </div>

              <!-- 标题与标签行容器 -->
              <div class="min-w-0 flex-1 space-y-1 sm:space-y-1.5">
                <!-- 第一行：标题 + 仅数据徽章 + 版本标签（单行自适应截断） -->
                <div class="flex items-center gap-1.5 sm:gap-2 min-w-0">
                  <span
                    class="text-sm sm:text-base font-semibold text-slate-800 dark:text-slate-100 truncate min-w-0 transition-colors group-hover:text-fnos-blue dark:group-hover:text-blue-400"
                    :title="item.name">
                    {{ item.name }}
                  </span>

                  <!-- 语义化版本标签 (Semver) -->
                  <n-tag size="tiny" :bordered="false"
                    class="shrink-0 font-mono text-[10px] sm:text-xs bg-slate-100 dark:bg-white/[0.08] text-slate-600 dark:text-slate-300">
                    {{ formatSnapshotVersion(item) }}
                  </n-tag>

                  <!-- 旧版不兼容标签 -->
                  <n-tag v-if="isLegacySnapshot(item)" type="warning" size="tiny" :bordered="false"
                    class="shrink-0 text-[10px] sm:text-xs">
                    不兼容
                  </n-tag>
                </div>

                  <!-- 创建时间与归档大小 -->
                <div
                  class="text-[11px] sm:text-xs text-slate-400 dark:text-slate-500 flex items-center flex-wrap gap-x-2.5 gap-y-0.5">
                  <div class="flex items-center gap-1 shrink-0">
                    <n-icon :size="12">
                      <Clock />
                    </n-icon>
                    <span>{{ formatTime(item.created_at) }}</span>
                  </div>
                  <span>·</span>
                  <span>{{ formatSize(item.size_bytes) }}</span>
                </div>
              </div>
            </div>

            <!-- 右侧 / 移动端底部操作栏 -->
            <div
              class="flex items-center justify-end gap-2 shrink-0 pt-2 border-t border-slate-100/80 dark:border-white/[0.04] sm:border-0 sm:pt-0">
              <n-tooltip v-if="isLegacySnapshot(item)" trigger="hover">
                <template #trigger>
                  <span>
                    <n-button secondary type="primary" size="small" disabled
                      class="!h-7 sm:!h-8 !px-2.5 sm:!px-3 rounded-lg text-xs font-medium opacity-50 cursor-not-allowed">
                      <template #icon>
                        <n-icon>
                          <Rotate />
                        </n-icon>
                      </template>
                      还原
                    </n-button>
                  </span>
                </template>
                该快照格式不受当前管理器支持
              </n-tooltip>

              <n-button v-else secondary type="primary" size="small" :disabled="actionsDisabled"
                @click="promptRestore(item)"
                class="!h-7 sm:!h-8 !px-2.5 sm:!px-3 rounded-lg text-xs font-medium">
                <template #icon>
                  <n-icon>
                    <Rotate />
                  </n-icon>
                </template>
                还原
              </n-button>

              <n-button secondary type="error" size="small" :disabled="actionsDisabled" @click="promptDelete(item)"
                class="!h-7 sm:!h-8 !px-2.5 sm:!px-3 rounded-lg text-xs font-medium">
                <template #icon>
                  <n-icon>
                    <Trash />
                  </n-icon>
                </template>
                删除
              </n-button>
            </div>
          </div>
        </n-card>
      </div>
    </div>
    <!-- 创建快照弹窗 -->
    <n-modal v-model:show="showCreateModal" preset="dialog" title="创建 OpenChamber 快照" positive-text="创建快照" negative-text="取消"
      :positive-button-props="{ disabled: actionsDisabled }" @positive-click="handleCreateSnapshot">
      <div class="space-y-3.5 pt-1 text-xs">
        <!-- 快照说明 -->
        <div
          class="p-2 rounded-xl bg-slate-50 dark:bg-white/[0.03] border border-slate-200/60 dark:border-white/[0.06] text-[11.5px] text-slate-500 dark:text-slate-400">
          备份持久化配置、模型凭据和会话，不包含项目源码、缓存及运行包。创建时暂停服务，完成后恢复原运行状态。
        </div>

        <div class="space-y-1.5">
          <div class="flex items-center justify-between font-medium text-slate-700 dark:text-slate-200">
            <span>快照名称</span>
            <span v-if="isDuplicateName" class="text-[11px] text-amber-500 font-normal">已存在同名快照</span>
          </div>
          <n-input v-model:value="snapName" placeholder="请输入快照名称" autofocus maxlength="40"
            :status="isDuplicateName ? 'warning' : undefined" />
        </div>
        <div class="space-y-1.5">
          <div class="font-medium text-slate-700 dark:text-slate-200">压缩级别</div>
          <n-select v-model:value="compressionLevel" :options="compressionOptions" />
        </div>
      </div>
    </n-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, computed } from 'vue'
import { storeToRefs } from 'pinia'
import {
  NAlert,
  NButton,
  NCard,
  NEmpty,
  NIcon,
  NTag,
  NInput,
  NSelect,
  NModal,
  NTooltip,
  useMessage,
  useDialog
} from 'naive-ui'
import {
  History,
  Clock,
  Trash,
  Rotate,
  X
} from '@vicons/tabler'
import { useSnapshotStore } from '../stores/snapshot'
import { useSystemStore } from '../stores/system'
import type { SnapshotMeta } from '../types/api'

const message = useMessage()
const dialog = useDialog()
const snapshotStore = useSnapshotStore()
const { wsUnavailable, isActionLocked } = storeToRefs(useSystemStore())

const {
  snapshots,
  totalSizeBytes,
  loading,
  listError,
  actionLoading,
  cancelLoading,
  task
} = storeToRefs(snapshotStore)
const actionsDisabled = computed(() => loading.value || Boolean(listError.value) || actionLoading.value || wsUnavailable.value || isActionLocked.value)

// 创建弹窗状态
const showCreateModal = ref(false)
const snapName = ref('')
const compressionLevel = ref(1)

const isDuplicateName = computed(() => {
  const name = snapName.value.trim().toLowerCase()
  if (!name) return false
  return snapshots.value.some(s => (s.name || '').trim().toLowerCase() === name)
})

const compressionOptions = [
  { label: '快速 (Lv 1)', value: 1 },
  { label: '标准 (Lv 6)', value: 6 },
  { label: '极限 (Lv 9)', value: 9 }
]

function formatSnapshotVersion(item: SnapshotMeta): string {
  return item.openchamber_version ? `v${item.openchamber_version} / OC ${item.opencode_version || '-'}` : '-'
}

function isLegacySnapshot(item: SnapshotMeta): boolean {
  return item.format_version !== 1
}

function openCreateModal() {
  const d = new Date()
  let baseName = `快照_${pad(d.getMonth() + 1)}${pad(d.getDate())}_${pad(d.getHours())}${pad(d.getMinutes())}`
  if (snapshots.value.some(s => (s.name || '').trim().toLowerCase() === baseName.toLowerCase())) {
    baseName += `_${pad(d.getSeconds())}`
  }
  snapName.value = baseName
  compressionLevel.value = 1
  showCreateModal.value = true
}

function handleCreateSnapshot() {
  if (actionsDisabled.value) return false
  const name = snapName.value.trim()
  if (!name) {
    message.warning('请输入快照名称')
    return false
  }
  if (isDuplicateName.value) {
    message.warning(`已存在同名快照「${name}」，请更换名称`)
    return false
  }

  showCreateModal.value = false

  void snapshotStore.createSnapshot({
    name,
    compression_level: compressionLevel.value
  }).then(res => {
    if (res.success) {
      message.info(res.message || '快照创建任务已启动...')
    } else {
      message.error(res.message || '创建快照失败')
    }
  }).catch((err: any) => {
    message.error(err?.message || '创建快照失败')
  })

  return true
}

function promptCancelCreate() {
  dialog.warning({
    title: '确认取消创建快照？',
    content: '取消后将中止正在进行的打包流程，清理未完成的快照数据并恢复服务。确定要取消吗？',
    positiveText: '确认取消',
    negativeText: '继续打包',
    positiveButtonProps: { type: 'error' },
    onPositiveClick: async () => {
      try {
        const res = await snapshotStore.cancelCreateSnapshot()
        if (res.success) {
          message.info(res.message || '正在取消快照创建...')
        } else {
          message.error(res.message || '取消失败')
        }
      } catch (err: any) {
        message.error(err?.message || '取消失败')
      }
    }
  })
}

function promptRestore(item: SnapshotMeta) {
  dialog.warning({
    title: '确认还原快照？',
    content: `确定还原快照「${item.name}」吗？将还原 OpenChamber ${item.openchamber_version} / OpenCode ${item.opencode_version} 对应的数据；运行版本不同时先联网安装精确版本。还原将覆盖持久化配置、模型凭据和会话，项目源码不受影响。失败时恢复操作前的数据与运行包，完成后恢复原运行状态。`,
    positiveText: '确认还原',
    negativeText: '取消',
    onPositiveClick: () => {
      void handleRestore(item.id)
    }
  })
}

function promptDelete(item: SnapshotMeta) {
  dialog.warning({
    title: '确认删除快照？',
    content: `确定永久删除快照「${item.name}」？此操作不可撤回。`,
    positiveText: '确认删除',
    negativeText: '取消',
    onPositiveClick: () => {
      void handleDelete(item.id)
    }
  })
}

async function handleRestore(id: string) {
  if (actionsDisabled.value) return
  try {
    const res = await snapshotStore.restoreSnapshot(id)
    if (res.success) {
      message.info(res.message || '快照还原任务已启动...')
    } else {
      message.error(res.message || '还原快照失败')
    }
  } catch (err: any) {
    message.error(err?.message || '还原快照失败')
  }
}

async function handleDelete(id: string) {
  if (actionsDisabled.value) return
  try {
    const res = await snapshotStore.deleteSnapshot(id)
    if (res.success) {
      message.success('已删除')
    } else {
      message.error(res.message || '删除失败')
    }
  } catch (err: any) {
    message.error(err?.message || '删除失败')
  }
}

function formatSize(bytes: number) {
  if (!bytes || bytes <= 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(k)), sizes.length - 1)
  return (bytes / Math.pow(k, i)).toFixed(1) + ' ' + sizes[i]
}

function formatTime(timestamp: number) {
  if (!timestamp) return '-'
  const d = new Date(timestamp * 1000)
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function pad(n: number) {
  return n < 10 ? '0' + n : String(n)
}

</script>
