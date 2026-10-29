<template>
  <div>
    <div class="grid" style="grid-template-columns: repeat(4, minmax(0, 1fr)); margin-bottom: 14px">
      <div class="metric">
        <div class="metric-head"><span>监控项</span></div>
        <div class="metric-value">{{ summary.total || 0 }}</div>
        <div class="metric-foot">启用中的检测任务</div>
      </div>
      <div class="metric">
        <div class="metric-head"><span>正常</span></div>
        <div class="metric-value" style="color: #1a7f47">{{ summary.up || 0 }}</div>
        <div class="metric-foot">最近一次检测通过</div>
      </div>
      <div class="metric">
        <div class="metric-head"><span>异常</span></div>
        <div class="metric-value" :style="{ color: summary.down ? '#c2382f' : 'inherit' }">{{ summary.down || 0 }}</div>
        <div class="metric-foot">已达失败阈值</div>
      </div>
      <div class="metric">
        <div class="metric-head"><span>未检测 / 波动</span></div>
        <div class="metric-value" style="color: #a86a12">{{ summary.pending || 0 }}</div>
        <div class="metric-foot">等待首次结果</div>
      </div>
    </div>

    <div class="toolbar">
      <el-button type="primary" :icon="Plus" @click="openEdit()">新增监控</el-button>
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <div class="spacer"></div>
      <span class="muted" style="font-size: 11.5px">告警渠道可在「设置」中配置</span>
    </div>

    <div class="card" style="padding: 0">
      <el-table :data="items" v-loading="loading" size="default" empty-text="还没有监控项，点击「新增监控」添加第一个站点">
        <el-table-column label="站点" min-width="200">
          <template #default="{ row }">
            <div class="row" style="gap: 8px">
              <span class="status-dot" :class="row.status"></span>
              <div>
                <div style="font-weight: 500">{{ row.name }}</div>
                <div class="mono muted" style="font-size: 11px">{{ row.target }}</div>
              </div>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="类型" width="90">
          <template #default="{ row }">
            <el-tag size="small" effect="plain">{{ typeText(row.type) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="130">
          <template #default="{ row }">
            <el-tag size="small" :type="statusType(row.status)">{{ statusText(row.status) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="响应" width="100">
          <template #default="{ row }">{{ row.latency_ms ? row.latency_ms + ' ms' : '-' }}</template>
        </el-table-column>
        <el-table-column label="24h 可用率" width="150">
          <template #default="{ row }">
            <div class="row" style="gap: 8px">
              <div class="progress" style="flex: 1; margin: 0">
                <i :style="{ width: (row.uptime || 0) + '%', background: (row.uptime || 0) > 99 ? '#2ba471' : '#e6a23c' }"></i>
              </div>
              <span class="muted" style="font-size: 11.5px">{{ num(row.uptime, 1) }}%</span>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="检测间隔" width="100">
          <template #default="{ row }"><span class="muted">{{ row.interval }}s</span></template>
        </el-table-column>
        <el-table-column label="最近检测" width="160">
          <template #default="{ row }">
            <span class="muted" style="font-size: 12px">{{ row.last_check_at ? fmtTime(row.last_check_at) : '尚未检测' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="276" align="right">
          <template #default="{ row }">
            <el-button
              link
              size="small"
              :type="row.enabled ? 'info' : 'success'"
              @click="toggle(row)"
            >
              {{ row.enabled ? '停止' : '启动' }}
            </el-button>
            <el-button link type="primary" size="small" @click="check(row)">检测</el-button>
            <el-button link type="primary" size="small" @click="openHistory(row)">历史</el-button>
            <el-button link type="primary" size="small" @click="openEdit(row)">编辑</el-button>
            <el-button link type="danger" size="small" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <el-dialog v-model="editVisible" :title="form.id ? '编辑监控' : '新增监控'" width="560px">
      <el-form :model="form" label-width="96px">
        <el-form-item label="名称" required><el-input v-model="form.name" placeholder="官网首页" /></el-form-item>
        <el-form-item label="检测类型">
          <el-radio-group v-model="form.type">
            <el-radio-button label="http">HTTP(S)</el-radio-button>
            <el-radio-button label="keyword">内容关键字</el-radio-button>
            <el-radio-button label="tcp">TCP 端口</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item :label="form.type === 'tcp' ? '目标' : 'URL'" required>
          <el-input v-model="form.target" :placeholder="form.type === 'tcp' ? '127.0.0.1:6379' : 'https://example.com'" />
        </el-form-item>
        <el-form-item v-if="form.type !== 'tcp'" label="期望状态码">
          <el-input-number v-model="form.expect_code" :min="0" :max="599" />
          <span class="muted" style="margin-left: 10px; font-size: 11.5px">填 200 表示接受 2xx/3xx</span>
        </el-form-item>
        <el-form-item v-if="form.type === 'keyword'" label="响应关键字">
          <el-input v-model="form.keyword" placeholder="页面必须包含的文字" />
        </el-form-item>
        <el-form-item label="检测间隔">
          <el-input-number v-model="form.interval" :min="10" :max="3600" />
          <span class="muted" style="margin-left: 10px">秒</span>
        </el-form-item>
        <el-form-item label="超时">
          <el-input-number v-model="form.timeout" :min="1" :max="60" />
          <span class="muted" style="margin-left: 10px">秒</span>
        </el-form-item>
        <el-form-item label="失败重试">
          <el-input-number v-model="form.fail_retries" :min="1" :max="10" />
          <span class="muted" style="margin-left: 10px">连续失败达到次数才判定宕机</span>
        </el-form-item>
        <el-form-item label="分组"><el-input v-model="form.group_name" placeholder="可选" /></el-form-item>
        <el-form-item label="启用"><el-switch v-model="form.enabled" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="editVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="historyVisible" :title="'响应时间 · ' + historyName" width="780px">
      <div class="row" style="margin-bottom: 10px">
        <el-radio-group v-model="historyHours" size="small" @change="loadHistory">
          <el-radio-button :label="6">6 小时</el-radio-button>
          <el-radio-button :label="24">24 小时</el-radio-button>
          <el-radio-button :label="168">7 天</el-radio-button>
        </el-radio-group>
        <div class="spacer"></div>
        <span class="muted" style="font-size: 12px">可用率 {{ num(historyUptime, 2) }}%</span>
      </div>
      <EChart :option="historyOption" height="300px" />
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh } from '@element-plus/icons-vue'
import EChart from '../components/EChart.vue'
import { api, errMsg } from '../api'
import { fmtClock, fmtTime, num } from '../utils'

const items = ref<any[]>([])
const summary = ref<any>({})
const loading = ref(false)

const editVisible = ref(false)
const saving = ref(false)
const form = reactive<any>({
  id: 0,
  name: '',
  type: 'http',
  target: '',
  method: 'GET',
  expect_code: 200,
  keyword: '',
  interval: 60,
  timeout: 10,
  fail_retries: 2,
  enabled: true,
  group_name: ''
})

const historyVisible = ref(false)
const historyName = ref('')
const historyHours = ref(24)
const historyPoints = ref<any[]>([])
const historyUptime = ref(100)
const historyId = ref(0)

function typeText(t: string) {
  return t === 'tcp' ? 'TCP' : t === 'keyword' ? '关键字' : 'HTTP'
}
function statusText(s: string) {
  if (s === 'up') return '正常'
  if (s === 'down') return '异常'
  if (s === 'pending') return '波动中'
  if (s === 'paused') return '已停止'
  return '未检测'
}
function statusType(s: string) {
  return s === 'up' ? 'success' : s === 'down' ? 'danger' : s === 'pending' ? 'warning' : 'info'
}

const historyOption = computed(() => {
  const pts = historyPoints.value
  const x = pts.map((p) => fmtClock(p.time * 1000))
  const fails = pts
    .map((p, i) => (p.ok ? null : [i, p.latency]))
    .filter((v) => v !== null)
  return {
    grid: { left: 46, right: 16, top: 20, bottom: 30 },
    tooltip: {
      trigger: 'axis',
      formatter: (arr: any[]) => {
        const i = arr[0]?.dataIndex
        const p = pts[i]
        if (!p) return ''
        return `${fmtTime(p.time)}<br/>${p.ok ? '正常' : '失败'} · ${p.latency}ms${p.code ? ' · ' + p.code : ''}`
      }
    },
    xAxis: {
      type: 'category',
      data: x,
      axisLabel: { fontSize: 10, color: '#8b94a7', interval: Math.max(1, Math.floor(x.length / 8)) },
      axisLine: { lineStyle: { color: '#e6e9f0' } },
      axisTick: { show: false }
    },
    yAxis: {
      type: 'value',
      name: 'ms',
      nameTextStyle: { fontSize: 10, color: '#8b94a7' },
      axisLabel: { fontSize: 10, color: '#8b94a7' },
      splitLine: { lineStyle: { color: '#f1f3f8' } }
    },
    series: [
      {
        type: 'line',
        smooth: true,
        showSymbol: false,
        data: pts.map((p) => p.latency),
        lineStyle: { width: 2, color: '#3b63e8' },
        areaStyle: {
          color: {
            type: 'linear',
            x: 0, y: 0, x2: 0, y2: 1,
            colorStops: [
              { offset: 0, color: 'rgba(59,99,232,0.18)' },
              { offset: 1, color: 'rgba(59,99,232,0.01)' }
            ]
          }
        }
      },
      {
        type: 'scatter',
        data: fails,
        symbolSize: 7,
        itemStyle: { color: '#e14b4b' }
      }
    ]
  }
})

async function load() {
  loading.value = true
  try {
    items.value = (await api<any>('/api/monitors')).items || []
    summary.value = await api<any>('/api/monitors/summary')
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

function openEdit(row?: any) {
  Object.assign(form, {
    id: row?.id || 0,
    name: row?.name || '',
    type: row?.type || 'http',
    target: row?.target || '',
    method: 'GET',
    expect_code: row?.expect_code || 200,
    keyword: row?.keyword || '',
    interval: row?.interval || 60,
    timeout: row?.timeout || 10,
    fail_retries: row?.fail_retries || 2,
    enabled: row ? row.enabled : true,
    group_name: row?.group_name || ''
  })
  editVisible.value = true
}

async function save() {
  if (!form.name || !form.target) return ElMessage.warning('请填写名称与检测目标')
  saving.value = true
  try {
    if (form.id) await api(`/api/monitors/${form.id}`, { method: 'PUT', body: { ...form } })
    else await api('/api/monitors', { body: { ...form } })
    ElMessage.success('已保存')
    editVisible.value = false
    load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
  }
}

// 启动 / 停止：只切调度开关，历史记录和配置都保留
async function toggle(row: any) {
  const next = !row.enabled
  try {
    await api(`/api/monitors/${row.id}/toggle`, { method: 'POST', body: { enabled: next } })
    ElMessage.success(next ? '已启动' : '已停止')
    load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function check(row: any) {
  try {
    const r = await api<any>(`/api/monitors/${row.id}/check`, { method: 'POST' })
    if (r.status === 'up') ElMessage.success(`检测通过，耗时 ${r.latency_ms}ms`)
    else ElMessage.warning(`检测失败：${r.last_message || '未知原因'}`)
    load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function remove(row: any) {
  try {
    await ElMessageBox.confirm(`删除监控「${row.name}」及其历史记录？`, '删除', { type: 'warning' })
  } catch {
    return
  }
  try {
    await api(`/api/monitors/${row.id}`, { method: 'DELETE' })
    ElMessage.success('已删除')
    load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function openHistory(row: any) {
  historyId.value = row.id
  historyName.value = row.name
  historyVisible.value = true
  await loadHistory()
}

async function loadHistory() {
  const d = await api<any>(`/api/monitors/${historyId.value}/history`, { query: { hours: historyHours.value } })
  historyPoints.value = d.points || []
  historyUptime.value = d.uptime ?? 100
}

onMounted(load)
</script>

<style scoped>
.status-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: #b4b2a9;
  flex: 0 0 auto;
}
.status-dot.up {
  background: #2ba471;
  box-shadow: 0 0 0 3px rgba(43, 164, 113, 0.14);
}
.status-dot.down {
  background: #e14b4b;
  box-shadow: 0 0 0 3px rgba(225, 75, 75, 0.14);
}
.status-dot.pending {
  background: #e6a23c;
}
</style>
