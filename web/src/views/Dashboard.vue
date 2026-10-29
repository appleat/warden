<template>
  <div>
    <div class="grid" style="grid-template-columns: repeat(4, minmax(0, 1fr))">
      <div class="metric">
        <div class="metric-head">
          <span>CPU 使用率</span>
          <span class="muted">{{ snap.host?.cpu_cores || 0 }} 核</span>
        </div>
        <div class="metric-value">
          {{ num(snap.cpu_percent) }}<small>%</small>
        </div>
        <div class="progress" :class="percentClass(snap.cpu_percent)">
          <i :style="{ width: Math.min(100, snap.cpu_percent || 0) + '%' }"></i>
        </div>
        <div class="metric-foot">负载 {{ num(snap.load1, 2) }} / {{ num(snap.load5, 2) }} / {{ num(snap.load15, 2) }}</div>
      </div>

      <div class="metric">
        <div class="metric-head">
          <span>内存占用</span>
          <span class="muted">{{ fmtBytes(snap.mem_total) }}</span>
        </div>
        <div class="metric-value">
          {{ num(snap.mem_percent) }}<small>%</small>
        </div>
        <div class="progress" :class="percentClass(snap.mem_percent)">
          <i :style="{ width: Math.min(100, snap.mem_percent || 0) + '%' }"></i>
        </div>
        <div class="metric-foot">已用 {{ fmtBytes(snap.mem_used) }}</div>
      </div>

      <div class="metric">
        <div class="metric-head">
          <span>磁盘占用</span>
          <span class="muted">{{ (snap.disks || []).length }} 个挂载点</span>
        </div>
        <div class="metric-value">
          {{ num(maxDisk) }}<small>%</small>
        </div>
        <div class="progress" :class="percentClass(maxDisk)">
          <i :style="{ width: Math.min(100, maxDisk) + '%' }"></i>
        </div>
        <div class="metric-foot">
          {{ mainDisk ? `${mainDisk.mountpoint} · 可用 ${fmtBytes(mainDisk.free)}` : '暂无数据' }}
        </div>
      </div>

      <div class="metric">
        <div class="metric-head">
          <span>网络吞吐</span>
          <span class="muted">{{ fmtBytes(snap.net_rx_bytes) }} 累计</span>
        </div>
        <div class="metric-value" style="font-size: 20px">
          {{ fmtRate((snap.net_rx_rate || 0) + (snap.net_tx_rate || 0)) }}
        </div>
        <div class="net-split">
          <div><span>入站</span><b>↓ {{ fmtRate(snap.net_rx_rate) }}</b></div>
          <div><span>出站</span><b>↑ {{ fmtRate(snap.net_tx_rate) }}</b></div>
        </div>
      </div>
    </div>

    <div class="grid" style="grid-template-columns: 1.55fr 1fr; margin-top: 14px">
      <div class="card">
        <div class="card-title">
          <span>资源趋势</span>
          <el-radio-group v-model="range" size="small">
            <el-radio-button label="cpu">CPU</el-radio-button>
            <el-radio-button label="mem">内存</el-radio-button>
            <el-radio-button label="net">网络</el-radio-button>
          </el-radio-group>
        </div>
        <EChart :option="trendOption" height="238px" />
      </div>

      <div class="card">
        <div class="card-title"><span>主机信息</span></div>
        <div class="kv">
          <div><span>主机名</span><b>{{ snap.host?.hostname || '-' }}</b></div>
          <div><span>系统</span><b>{{ snap.host?.platform || '-' }} {{ snap.host?.version || '' }}</b></div>
          <div><span>内核</span><b class="mono">{{ snap.host?.kernel || '-' }}</b></div>
          <div><span>架构</span><b>{{ snap.host?.arch || '-' }}</b></div>
          <div><span>运行时长</span><b>{{ fmtDuration(snap.host?.uptime) }}</b></div>
          <div><span>CPU 型号</span><b :title="snap.host?.cpu_model">{{ shortCpu }}</b></div>
          <div><span>总内存</span><b>{{ fmtBytes(snap.host?.total_mem) }}</b></div>
          <div><span>Swap</span><b>{{ snap.swap_total ? fmtBytes(snap.swap_used) + ' / ' + fmtBytes(snap.swap_total) : '未启用' }}</b></div>
        </div>
      </div>
    </div>

    <div class="grid" style="grid-template-columns: 1fr 1.55fr; margin-top: 14px">
      <div class="card">
        <div class="card-title">
          <span>Docker 与监控</span>
          <el-tag size="small" :type="docker.available ? 'success' : 'info'" effect="plain">
            {{ docker.available ? '已连接' : '未连接' }}
          </el-tag>
        </div>
        <div class="mini-grid">
          <div class="mini">
            <el-link type="text" @click="$router.push('/containers')">容器</el-link>
            <b>{{ docker.containers || 0 }}</b>
            <small class="muted">运行 {{ docker.running || 0 }}</small>
          </div>
          <div class="mini">
            <span class="muted">镜像</span>
            <b>{{ docker.images || 0 }}</b>
            <small class="muted">Docker {{ docker.version || '-' }}</small>
          </div>
          <div class="mini">
            <span class="muted">监控站点</span>
            <b>{{ monitor.total || 0 }}</b>
            <small class="muted">正常 {{ monitor.up || 0 }}</small>
          </div>
          <div class="mini">
            <span class="muted">异常站点</span>
            <b :style="{ color: monitor.down ? '#c2382f' : 'inherit' }">{{ monitor.down || 0 }}</b>
            <small class="muted">待检测 {{ monitor.pending || 0 }}</small>
          </div>
        </div>
        <div v-if="!docker.available && docker.error" class="muted" style="margin-top: 10px; font-size: 11.5px; line-height: 1.6">
          {{ docker.error }}
        </div>
        <div style="margin-top: 12px">
          <div v-for="d in (snap.disks || []).slice(0, 4)" :key="d.mountpoint" class="disk-row">
            <span class="mono">{{ d.mountpoint }}</span>
            <div class="progress" :class="percentClass(d.percent)" style="flex: 1">
              <i :style="{ width: Math.min(100, d.percent) + '%' }"></i>
            </div>
            <span class="muted" style="width: 118px; text-align: right; white-space: nowrap">
              {{ fmtBytes(d.used) }} / {{ fmtBytes(d.total) }}
            </span>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="card-title">
          <span>CPU 占用 Top 进程</span>
          <el-button link type="primary" size="small" @click="loadProcs">刷新</el-button>
        </div>
        <el-table :data="procs" size="small" style="width: 100%" :show-header="true">
          <el-table-column prop="name" label="进程" min-width="140" show-overflow-tooltip />
          <el-table-column prop="pid" label="PID" width="72" />
          <el-table-column label="CPU" width="80">
            <template #default="{ row }">{{ num(row.cpu) }}%</template>
          </el-table-column>
          <el-table-column label="内存" width="86">
            <template #default="{ row }">{{ fmtBytes(row.rss) }}</template>
          </el-table-column>
          <el-table-column prop="user" label="用户" width="90" show-overflow-tooltip />
        </el-table>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import EChart from '../components/EChart.vue'
import { api, wsURL } from '../api'
import { fmtBytes, fmtClock, fmtDuration, fmtRate, num, percentClass } from '../utils'

const snap = ref<any>({})
const history = ref<any[]>([])
const docker = ref<any>({})
const monitor = ref<any>({})
const procs = ref<any[]>([])
const range = ref('cpu')
let ws: WebSocket | null = null

const mainDisk = computed(() => {
  const ds = snap.value.disks || []
  return ds.length ? ds.reduce((a: any, b: any) => (b.percent > a.percent ? b : a)) : null
})
const maxDisk = computed(() => mainDisk.value?.percent || 0)
const shortCpu = computed(() => {
  const m = snap.value.host?.cpu_model || '-'
  return m.length > 26 ? m.slice(0, 26) + '…' : m
})

const trendOption = computed(() => {
  const hist = history.value
  const x = hist.map((h) => fmtClock(h.t))
  let series: any[] = []
  if (range.value === 'cpu') {
    series = [
      {
        name: 'CPU %',
        type: 'line',
        smooth: true,
        showSymbol: false,
        data: hist.map((h) => h.cpu),
        lineStyle: { width: 2, color: '#3b63e8' },
        areaStyle: {
          color: {
            type: 'linear',
            x: 0, y: 0, x2: 0, y2: 1,
            colorStops: [
              { offset: 0, color: 'rgba(59,99,232,0.22)' },
              { offset: 1, color: 'rgba(59,99,232,0.01)' }
            ]
          }
        }
      }
    ]
  } else if (range.value === 'mem') {
    series = [
      {
        name: '内存 %',
        type: 'line',
        smooth: true,
        showSymbol: false,
        data: hist.map((h) => h.mem),
        lineStyle: { width: 2, color: '#1d9e75' },
        areaStyle: {
          color: {
            type: 'linear',
            x: 0, y: 0, x2: 0, y2: 1,
            colorStops: [
              { offset: 0, color: 'rgba(29,158,117,0.2)' },
              { offset: 1, color: 'rgba(29,158,117,0.01)' }
            ]
          }
        }
      }
    ]
  } else {
    series = [
      {
        name: '入站',
        type: 'line',
        smooth: true,
        showSymbol: false,
        data: hist.map((h) => Math.round((h.net_rx || 0) / 1024)),
        lineStyle: { width: 2, color: '#3b63e8' }
      },
      {
        name: '出站',
        type: 'line',
        smooth: true,
        showSymbol: false,
        data: hist.map((h) => Math.round((h.net_tx || 0) / 1024)),
        lineStyle: { width: 2, color: '#e6a23c' }
      }
    ]
  }
  return {
    grid: { left: 42, right: 16, top: 26, bottom: 26 },
    tooltip: { trigger: 'axis' },
    legend: { show: range.value === 'net', right: 0, top: 0, itemHeight: 8, itemWidth: 12, textStyle: { fontSize: 11 } },
    xAxis: {
      type: 'category',
      data: x,
      boundaryGap: false,
      axisLine: { lineStyle: { color: '#e6e9f0' } },
      axisLabel: { color: '#8b94a7', fontSize: 10, interval: Math.max(1, Math.floor(x.length / 6)) },
      axisTick: { show: false }
    },
    yAxis: {
      type: 'value',
      max: range.value === 'net' ? undefined : 100,
      axisLabel: {
        color: '#8b94a7',
        fontSize: 10,
        formatter: (v: number) => (range.value === 'net' ? `${v}K` : `${v}%`)
      },
      splitLine: { lineStyle: { color: '#f1f3f8' } }
    },
    series
  }
})

async function load() {
  const d = await api<any>('/api/dashboard/overview')
  snap.value = d.metrics || {}
  history.value = (d.history || []).slice(-120)
  docker.value = d.docker || {}
  monitor.value = d.monitor || {}
}

async function loadProcs() {
  const d = await api<any>('/api/dashboard/processes', { query: { limit: 8 } })
  procs.value = d.items || []
}

function connect() {
  ws = new WebSocket(wsURL('/ws/metrics'))
  ws.onmessage = (ev) => {
    try {
      const msg = JSON.parse(ev.data)
      if (msg.type !== 'metrics') return
      snap.value = msg.data
      const h = {
        t: msg.data.time,
        cpu: msg.data.cpu_percent,
        mem: msg.data.mem_percent,
        net_rx: msg.data.net_rx_rate,
        net_tx: msg.data.net_tx_rate
      }
      history.value = [...history.value.slice(-179), h]
    } catch {}
  }
  ws.onclose = () => setTimeout(connect, 3000)
}

let procTimer: any = null

onMounted(async () => {
  await load()
  await loadProcs()
  connect()
  procTimer = setInterval(loadProcs, 15000)
})

onBeforeUnmount(() => {
  if (procTimer) clearInterval(procTimer)
  if (ws) {
    ws.onclose = null
    ws.close()
  }
})
</script>

<style scoped>
.kv > div {
  display: flex;
  justify-content: space-between;
  padding: 7px 0;
  border-bottom: 1px dashed #f0f2f7;
  font-size: 12.5px;
}
.kv > div:last-child {
  border-bottom: none;
}
.kv span {
  color: var(--ink-3);
}
.kv b {
  font-weight: 500;
  color: var(--ink);
  max-width: 62%;
  text-align: right;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.mini-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 10px;
}
.mini {
  background: #f8f9fc;
  border-radius: 8px;
  padding: 10px 12px;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.mini b {
  font-size: 19px;
  font-weight: 600;
}
.mini small {
  font-size: 11px;
}
.net-split {
  display: flex;
  gap: 10px;
  margin-top: 10px;
}
.net-split > div {
  flex: 1;
  background: #f8f9fc;
  border-radius: 7px;
  padding: 6px 9px;
  display: flex;
  flex-direction: column;
  gap: 1px;
}
.net-split span {
  font-size: 10.5px;
  color: var(--ink-3);
}
.net-split b {
  font-size: 12px;
  font-weight: 500;
  color: var(--ink);
}
.disk-row {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 11.5px;
  margin-bottom: 8px;
}
</style>
