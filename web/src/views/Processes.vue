<template>
  <div>
    <!-- 工具栏 -->
    <div class="card" style="padding: 14px 16px">
      <div class="row" style="flex-wrap: wrap; gap: 8px">
        <el-input
          v-model="keyword"
          placeholder="按进程名过滤"
          clearable
          style="width: 210px"
          :prefix-icon="Search"
        />
        <el-select v-model="limit" style="width: 128px">
          <el-option :value="50" label="前 50 条" />
          <el-option :value="100" label="前 100 条" />
          <el-option :value="200" label="前 200 条" />
          <el-option :value="500" label="前 500 条" />
        </el-select>
        <span class="stat-pill" :class="total ? 'ok' : 'warn'">
          <span class="dot" :class="total ? 'green' : 'yellow'"></span>
          {{ total }} 个进程<template v-if="total > rows.length">，显示前 {{ rows.length }}</template>
        </span>
        <div class="spacer"></div>
        <el-switch v-model="auto" active-text="自动刷新" />
        <span class="muted" style="font-size: 12px">
          {{ updatedAt ? `更新于 ${updatedAt}` : '尚未加载' }}
        </span>
        <el-button :icon="Refresh" :loading="loading" @click="load(false)">刷新</el-button>
      </div>
      <div class="muted" style="font-size: 12px; margin-top: 10px; line-height: 1.8">
        CPU 为单核口径（100% = 占满一个核心，多核进程可超过 100%），每 {{ interval }} ms 采样一次；
        点击表头可切换排序字段，进程名与 PID 支持升序。
      </div>
    </div>

    <!-- 进程表 -->
    <div class="card" style="padding: 0; margin-top: 12px">
      <el-table
        :data="rows"
        size="small"
        v-loading="loading"
        max-height="calc(100vh - 300px)"
        :default-sort="{ prop: 'cpu', order: 'descending' }"
        empty-text="没有匹配的进程"
        style="width: 100%"
        @sort-change="onSortChange"
        @row-click="openDetail"
      >
        <el-table-column prop="pid" label="PID" width="90" sortable="custom">
          <template #default="{ row }">
            <span class="mono">{{ row.pid }}</span>
          </template>
        </el-table-column>

        <el-table-column prop="name" label="进程名" min-width="180" sortable="custom" show-overflow-tooltip>
          <template #default="{ row }">
            <span style="font-weight: 500">{{ row.name }}</span>
            <el-tag v-if="row.self" size="small" type="primary" effect="plain" style="margin-left: 6px">
              Warden
            </el-tag>
          </template>
        </el-table-column>

        <el-table-column prop="user" label="用户" width="100" show-overflow-tooltip>
          <template #default="{ row }">
            <span class="mono muted" style="font-size: 12px">{{ row.user || '-' }}</span>
          </template>
        </el-table-column>

        <el-table-column prop="status" label="状态" width="92">
          <template #default="{ row }">
            <el-tag size="small" effect="plain" :type="statusType(row.status)">
              {{ statusText(row.status) }}
            </el-tag>
          </template>
        </el-table-column>

        <el-table-column prop="cpu" label="CPU%" width="96" align="right" sortable="custom">
          <template #default="{ row }">
            <span class="mono" :class="cpuClass(row.cpu)">{{ row.cpu.toFixed(1) }}</span>
          </template>
        </el-table-column>

        <el-table-column prop="mem" label="内存%" width="96" align="right" sortable="custom">
          <template #default="{ row }">
            <span class="mono" :class="percentClass(row.mem_pct)">{{ row.mem_pct.toFixed(1) }}</span>
          </template>
        </el-table-column>

        <el-table-column prop="rss" label="常驻内存" width="110" align="right" sortable="custom">
          <template #default="{ row }">
            <span class="mono" style="font-size: 12px">{{ fmtBytes(row.rss) }}</span>
          </template>
        </el-table-column>

        <el-table-column prop="threads" label="线程" width="82" align="right" sortable="custom">
          <template #default="{ row }">
            <span class="mono muted">{{ row.threads }}</span>
          </template>
        </el-table-column>

        <el-table-column prop="uptime" label="运行时长" width="118" sortable="custom">
          <template #default="{ row }">
            <span style="font-size: 12px">{{ uptime(row.uptime_sec) }}</span>
          </template>
        </el-table-column>

        <el-table-column prop="cmdline" label="命令行" min-width="240" show-overflow-tooltip>
          <template #default="{ row }">
            <span class="mono muted" style="font-size: 11.5px">{{ row.cmdline || row.exe || '-' }}</span>
          </template>
        </el-table-column>

        <el-table-column label="操作" width="150" align="right" fixed="right">
          <template #default="{ row }">
            <template v-if="row.self">
              <span class="muted" style="font-size: 12px">受保护</span>
            </template>
            <template v-else-if="row.pid <= 1">
              <span class="muted" style="font-size: 12px">受保护</span>
            </template>
            <template v-else>
              <el-button link type="warning" size="small" @click.stop="kill(row, false)">结束</el-button>
              <el-button link type="danger" size="small" @click.stop="kill(row, true)">强杀</el-button>
            </template>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 进程详情 -->
    <el-drawer v-model="detailVisible" :title="detail ? `${detail.name}（PID ${detail.pid}）` : '进程详情'" size="560px">
      <template v-if="detail">
        <el-descriptions :column="2" border size="small">
          <el-descriptions-item label="PID"><span class="mono">{{ detail.pid }}</span></el-descriptions-item>
          <el-descriptions-item label="父进程"><span class="mono">{{ detail.ppid }}</span></el-descriptions-item>
          <el-descriptions-item label="用户">{{ detail.user || '-' }}</el-descriptions-item>
          <el-descriptions-item label="状态">{{ statusText(detail.status) }} ({{ detail.status || '-' }})</el-descriptions-item>
          <el-descriptions-item label="CPU">{{ detail.cpu.toFixed(2) }}%</el-descriptions-item>
          <el-descriptions-item label="累计 CPU">{{ fmtCpuTime(detail.cpu_time) }}</el-descriptions-item>
          <el-descriptions-item label="内存占比">{{ detail.mem_pct.toFixed(2) }}%</el-descriptions-item>
          <el-descriptions-item label="常驻内存">{{ fmtBytes(detail.rss) }}</el-descriptions-item>
          <el-descriptions-item label="虚拟内存">{{ fmtBytes(detail.vms) }}</el-descriptions-item>
          <el-descriptions-item label="线程数">{{ detail.threads }}</el-descriptions-item>
          <el-descriptions-item label="启动时间">{{ fmtTime(detail.start_at) }}</el-descriptions-item>
          <el-descriptions-item label="运行时长">{{ uptime(detail.uptime_sec) }}</el-descriptions-item>
        </el-descriptions>

        <div style="margin-top: 18px">
          <div class="row" style="margin-bottom: 6px">
            <span style="font-size: 13px; font-weight: 600">可执行文件</span>
            <div class="spacer"></div>
            <el-button link size="small" @click="copy(detail.exe)">复制</el-button>
          </div>
          <div class="detail-box mono">{{ detail.exe || '-' }}</div>

          <div class="row" style="margin: 16px 0 6px">
            <span style="font-size: 13px; font-weight: 600">完整命令行</span>
            <div class="spacer"></div>
            <el-button link size="small" @click="copy(detail.cmdline)">复制</el-button>
          </div>
          <div class="detail-box mono">{{ detail.cmdline || '-' }}</div>
        </div>

        <div class="danger-zone">
          <div>
            <div style="font-size: 13px; font-weight: 600">结束该进程</div>
            <div class="muted" style="font-size: 12px; margin-top: 3px">
              SIGTERM 允许进程保存数据后退出；SIGKILL 立即终止，无法被捕获。
            </div>
          </div>
          <div v-if="detail.self || detail.pid <= 1" class="muted" style="font-size: 12px">受保护</div>
          <div v-else class="row" style="gap: 8px">
            <el-button size="small" type="warning" plain @click="kill(detail, false)">结束</el-button>
            <el-button size="small" type="danger" @click="kill(detail, true)">强制结束</el-button>
          </div>
        </div>
      </template>
    </el-drawer>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Refresh, Search } from '@element-plus/icons-vue'
import { api } from '../api'
import { copyText, fmtBytes, fmtDuration, fmtTime, percentClass } from '../utils'

interface ProcRow {
  pid: number
  ppid: number
  name: string
  user: string
  status: string
  cpu: number
  cpu_time: number
  mem_pct: number
  rss: number
  vms: number
  threads: number
  start_at: number
  uptime_sec: number
  cmdline: string
  exe: string
  self: boolean
}

const rows = ref<ProcRow[]>([])
const total = ref(0)
const loading = ref(false)
const interval = ref(300)
const keyword = ref('')
const limit = ref(100)
const auto = ref(false)
const updatedAt = ref('')
const sortKey = ref('cpu')
const sortOrder = ref<'asc' | 'desc'>('desc')

const detailVisible = ref(false)
const detail = ref<ProcRow | null>(null)

let timer: ReturnType<typeof setInterval> | undefined
let searchTimer: ReturnType<typeof setTimeout> | undefined

async function load(silent: boolean) {
  if (!silent) loading.value = true
  try {
    const data = await api<any>('/api/system/processes', {
      query: {
        sort: sortKey.value,
        order: sortOrder.value,
        limit: limit.value,
        q: keyword.value.trim()
      }
    })
    rows.value = data?.items || []
    total.value = data?.total || 0
    interval.value = data?.interval || interval.value
    updatedAt.value = new Date().toLocaleTimeString('zh-CN', { hour12: false })
  } catch (e: any) {
    if (!silent) ElMessage.error(e?.message || '读取进程列表失败')
  } finally {
    loading.value = false
  }
}

function onSortChange({ prop, order }: { prop: string; order: string | null }) {
  if (!prop || !order) {
    sortKey.value = 'cpu'
    sortOrder.value = 'desc'
  } else {
    sortKey.value = prop
    sortOrder.value = order === 'ascending' ? 'asc' : 'desc'
  }
  load(false)
}

async function kill(row: ProcRow, force: boolean) {
  if (row.self || row.pid <= 1) {
    ElMessage.warning('该进程受保护，不能结束')
    return
  }
  const how = force ? '强制结束（SIGKILL）' : '结束（SIGTERM）'
  try {
    await ElMessageBox.confirm(
      `确定要${how}进程 ${row.name}（PID ${row.pid}）吗？${
        force ? '该操作立即生效，未保存的数据会丢失。' : '进程会收到退出信号，可选择优雅退出。'
      }`,
      '结束进程',
      { type: 'warning', confirmButtonText: '确定', cancelButtonText: '取消' }
    )
  } catch {
    return
  }
  try {
    const res = await api<any>('/api/system/processes/kill', {
      method: 'POST',
      body: { pid: row.pid, force }
    })
    ElMessage.success(res?.msg || '已发送结束信号')
    if (detail.value?.pid === row.pid) detailVisible.value = false
    // 留一点时间让进程真正退出，列表刷新后才能看到结果
    setTimeout(() => load(true), 700)
  } catch (e: any) {
    ElMessage.error(e?.message || '操作失败')
  }
}

function openDetail(row: ProcRow) {
  detail.value = row
  detailVisible.value = true
}

async function copy(text: string) {
  if (!text) return
  const ok = await copyText(text)
  ok ? ElMessage.success('已复制') : ElMessage.error('复制失败')
}

const STATUS_TEXT: Record<string, string> = {
  R: '运行中',
  S: '休眠',
  D: '不可中断',
  Z: '僵尸',
  T: '已停止',
  t: '跟踪停止',
  I: '空闲',
  W: '换页中',
  X: '已退出'
}

function statusText(s?: string) {
  if (!s) return '-'
  return STATUS_TEXT[s] || s
}

function statusType(s?: string): 'success' | 'info' | 'warning' | 'danger' {
  switch (s) {
    case 'R':
      return 'success'
    case 'D':
    case 'T':
    case 't':
      return 'warning'
    case 'Z':
      return 'danger'
    default:
      return 'info'
  }
}

function cpuClass(v: number) {
  if (v >= 80) return 'cpu-high'
  if (v >= 30) return 'cpu-mid'
  return ''
}

function uptime(sec: number) {
  if (!sec || sec <= 0) return '-'
  if (sec < 60) return `${sec} 秒`
  if (sec < 3600) return `${Math.floor(sec / 60)} 分钟`
  return fmtDuration(sec)
}

function fmtCpuTime(sec: number) {
  if (!sec || sec <= 0) return '0s'
  if (sec < 60) return `${sec.toFixed(1)}s`
  if (sec < 3600) return `${Math.floor(sec / 60)}m ${Math.floor(sec % 60)}s`
  return `${Math.floor(sec / 3600)}h ${Math.floor((sec % 3600) / 60)}m`
}

watch(auto, (on) => {
  if (timer) {
    clearInterval(timer)
    timer = undefined
  }
  if (on) timer = setInterval(() => load(true), 4000)
})

watch(keyword, () => {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => load(false), 300)
})

watch(limit, () => load(false))

onMounted(() => load(false))

onBeforeUnmount(() => {
  if (timer) clearInterval(timer)
  if (searchTimer) clearTimeout(searchTimer)
})
</script>

<style scoped>
.cpu-high {
  color: #e34d59;
  font-weight: 600;
}
.cpu-mid {
  color: #d98600;
}
.detail-box {
  background: #f7f8fb;
  border: 1px solid var(--line);
  border-radius: 8px;
  padding: 10px 12px;
  font-size: 12px;
  line-height: 1.8;
  word-break: break-all;
  white-space: pre-wrap;
  color: var(--ink-2);
}
.danger-zone {
  margin-top: 22px;
  padding: 14px 16px;
  border: 1px solid #f5c6c6;
  background: #fdf3f3;
  border-radius: 10px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
</style>
