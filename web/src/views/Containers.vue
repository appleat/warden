<template>
  <div>
    <div class="card" v-if="dockerErr" style="border-color: #f3d6d6; background: #fffbfb; margin-bottom: 14px">
      <div class="row" style="align-items: flex-start">
        <el-icon style="color: #c2382f; margin-top: 2px"><WarningFilled /></el-icon>
        <div>
          <div style="font-weight: 500; margin-bottom: 3px">Docker 暂不可用</div>
          <div class="muted" style="font-size: 12px; line-height: 1.7">{{ dockerErr }}</div>
          <div class="muted" style="font-size: 11.5px; margin-top: 4px">
            请确认 Docker 已启动，且当前用户有访问 <code class="mono">/var/run/docker.sock</code> 的权限
          </div>
        </div>
      </div>
    </div>

    <div class="toolbar">
      <el-input v-model="q" placeholder="搜索名称 / 镜像 / ID" clearable style="width: 240px" @input="onSearch">
        <template #prefix><el-icon><Search /></el-icon></template>
      </el-input>
      <el-radio-group v-model="stateFilter" size="small">
        <el-radio-button label="all">全部</el-radio-button>
        <el-radio-button label="running">运行中</el-radio-button>
        <el-radio-button label="stopped">已停止</el-radio-button>
      </el-radio-group>
      <el-radio-group v-model="tab" size="small" style="margin-left: 6px">
        <el-radio-button label="containers">容器</el-radio-button>
        <el-radio-button label="images">镜像</el-radio-button>
        <el-radio-button label="networks">网络 / 卷</el-radio-button>
      </el-radio-group>
      <div class="spacer"></div>
      <el-button :icon="Refresh" @click="loadAll">刷新</el-button>
      <el-button v-if="tab === 'containers'" type="primary" :icon="Plus" @click="openCreate">新建容器</el-button>
      <el-button v-if="tab === 'images'" type="primary" :icon="Download" @click="pullVisible = true">拉取镜像</el-button>
    </div>

    <div v-show="tab === 'containers'" class="card" style="padding: 0">
      <el-alert
        v-if="statsUnavailable"
        type="warning"
        :closable="false"
        show-icon
        style="margin: 10px 12px 0"
        title="未获取到容器的 CPU / 内存数据"
      >
        <div style="font-size: 12px; line-height: 1.7">
          {{ statsUnavailableReason }}
        </div>
      </el-alert>
      <template v-for="g in groupedList" :key="g.key">
        <div class="stack-bar" @click="toggleGroup(g.key)">
          <el-icon class="stack-chevron" :class="{ collapsed: collapsedGroups[g.key] }"><CaretRight /></el-icon>
          <span class="stack-name" :class="{ loose: !g.stack }">{{ g.title }}</span>
          <el-tag size="small" :type="g.running ? 'success' : 'info'" effect="light">
            {{ g.running }}/{{ g.containers.length }} 运行中
          </el-tag>
          <span v-if="g.dir" class="muted mono stack-dir">{{ g.dir }}</span>
          <div class="spacer"></div>
          <el-button v-if="g.stack" size="small" @click.stop="openCompose(g.stack)">
            <el-icon style="margin-right: 4px"><EditPen /></el-icon>编辑 Compose
          </el-button>
        </div>
        <el-table
          v-show="!collapsedGroups[g.key]"
          :data="g.containers"
          v-loading="loading"
          size="default"
          style="width: 100%"
          :empty-text="emptyText"
          @row-click="openDetail"
          row-style="cursor:pointer"
        >
        <el-table-column label="名称" min-width="190">
          <template #default="{ row }">
            <div class="row">
              <span class="state-dot" :class="row.state"></span>
              <div>
                <div style="font-weight: 500">{{ row.name || row.id.slice(0, 12) }}</div>
                <div class="muted" style="font-size: 11px">{{ row.id.slice(0, 12) }}</div>
              </div>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="镜像" min-width="150" show-overflow-tooltip>
          <template #default="{ row }"><span class="mono" style="font-size: 12px">{{ row.image }}</span></template>
        </el-table-column>
        <el-table-column label="状态" width="108">
          <template #default="{ row }">
            <el-tag size="small" :type="stateType(row.state)" effect="light">{{ stateText(row.state) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="CPU" width="100">
          <template #default="{ row }">
            <div v-if="row.state !== 'running'" class="muted">-</div>
            <div v-else-if="!statsKnown(row)" class="muted" :title="statsLoading ? '正在采集资源占用…' : statsTip(row)">
              {{ statsLoading ? '…' : '-' }}
            </div>
            <div v-else class="cell-metric" :title="'CPU 占用 ' + num(row.cpu_percent, 1) + '%'">
              <div>{{ num(row.cpu_percent, 1) }}<span class="muted">%</span></div>
              <div class="progress" :class="percentClass(row.cpu_percent || 0)">
                <i :style="{ width: Math.min(100, row.cpu_percent || 0) + '%' }"></i>
              </div>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="内存" width="140">
          <template #default="{ row }">
            <div v-if="row.state !== 'running'" class="muted">-</div>
            <div v-else-if="!statsKnown(row)" class="muted" :title="statsLoading ? '正在采集资源占用…' : statsTip(row)">
              {{ statsLoading ? '…' : '-' }}
            </div>
            <div
              v-else
              class="cell-metric"
              :title="`已用 ${fmtBytes(row.mem_usage)} / 上限 ${fmtBytes(row.mem_limit)}`"
            >
              <div>
                {{ fmtBytes(row.mem_usage) }}<span class="muted"> · {{ num(row.mem_percent, 1) }}%</span>
              </div>
              <div class="progress" :class="percentClass(row.mem_percent || 0)">
                <i :style="{ width: Math.min(100, row.mem_percent || 0) + '%' }"></i>
              </div>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="端口" min-width="150">
          <template #default="{ row }">
            <span v-if="!row.ports?.length" class="muted">-</span>
            <template v-else>
              <!-- key 必须唯一：同一端口会在 IPv4/IPv6 上重复出现，重复 key 会让表格刷新时叠加渲染 -->
              <span v-for="(p, i) in row.ports.slice(0, 2)" :key="i" class="port-chip mono">
                {{ p.public_port ? p.public_port + '→' : '' }}{{ p.private_port }}/{{ p.type }}
              </span>
              <el-tooltip v-if="row.ports.length > 2" placement="top">
                <template #content>
                  <div v-for="(p, i) in row.ports" :key="i" class="mono" style="font-size: 12px">
                    {{ p.public_port ? p.public_port + '→' : '' }}{{ p.private_port }}/{{ p.type }}
                  </div>
                </template>
                <span class="port-chip mono">+{{ row.ports.length - 2 }}</span>
              </el-tooltip>
            </template>
          </template>
        </el-table-column>
        <el-table-column label="创建时间" width="140">
          <template #default="{ row }"><span class="muted">{{ fmtTime(row.created) }}</span></template>
        </el-table-column>
        <el-table-column label="操作" width="180" align="right">
          <template #default="{ row }">
            <el-button v-if="row.state !== 'running'" link type="primary" size="small" @click.stop="act(row, 'start')">启动</el-button>
            <el-button v-else link type="warning" size="small" @click.stop="act(row, 'stop')">停止</el-button>
            <el-button link type="primary" size="small" @click.stop="act(row, 'restart')">重启</el-button>
            <el-button link type="danger" size="small" @click.stop="remove(row)">删除</el-button>
          </template>
        </el-table-column>
        </el-table>
      </template>
    </div>

    <div v-show="tab === 'images'" class="card" style="padding: 0">
      <el-table :data="images" v-loading="loading" size="default" empty-text="暂无镜像，可点击「拉取镜像」">
        <el-table-column label="仓库标签" min-width="260">
          <template #default="{ row }">
            <div v-for="t in row.tags" :key="t" class="mono" style="font-size: 12.5px">{{ t }}</div>
          </template>
        </el-table-column>
        <el-table-column label="镜像 ID" width="140">
          <template #default="{ row }"><span class="mono muted">{{ row.id }}</span></template>
        </el-table-column>
        <el-table-column label="大小" width="110">
          <template #default="{ row }">{{ fmtBytes(row.size) }}</template>
        </el-table-column>
        <el-table-column label="创建时间" width="160">
          <template #default="{ row }"><span class="muted">{{ fmtTime(row.created) }}</span></template>
        </el-table-column>
        <el-table-column label="操作" width="100" align="right">
          <template #default="{ row }">
            <el-button link type="danger" size="small" @click="removeImage(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <div v-show="tab === 'networks'" class="grid" style="grid-template-columns: 1fr 1fr">
      <div class="card" style="padding: 0">
        <div class="card-title" style="padding: 14px 16px 10px; margin: 0">网络</div>
        <el-table :data="networks" size="small" empty-text="暂无网络">
          <el-table-column prop="name" label="名称" min-width="140" />
          <el-table-column prop="driver" label="驱动" width="90" />
          <el-table-column prop="scope" label="范围" width="80" />
          <el-table-column prop="containers" label="容器数" width="80" />
        </el-table>
      </div>
      <div class="card" style="padding: 0">
        <div class="card-title" style="padding: 14px 16px 10px; margin: 0">数据卷</div>
        <el-table :data="volumes" size="small" empty-text="暂无数据卷">
          <el-table-column prop="name" label="名称" min-width="160" show-overflow-tooltip />
          <el-table-column prop="driver" label="驱动" width="90" />
          <el-table-column prop="mountpoint" label="挂载点" min-width="160" show-overflow-tooltip />
        </el-table>
      </div>
    </div>

    <!-- 容器详情 -->
    <el-drawer v-model="drawer" size="76%" :with-header="true" :title="current?.name || '容器详情'">
      <div v-if="current" style="height: 100%; display: flex; flex-direction: column">
        <div class="row" style="margin-bottom: 12px; flex-wrap: wrap">
          <el-tag size="small" :type="stateType(current.state)">{{ stateText(current.state) }}</el-tag>
          <span class="muted mono" style="font-size: 11.5px">{{ current.id.slice(0, 20) }}</span>
          <span class="muted" style="font-size: 12px">{{ current.image }}</span>
          <div class="spacer"></div>
          <el-button size="small" @click="act(current!, 'start')" :disabled="current.state === 'running'">启动</el-button>
          <el-button size="small" @click="act(current!, 'stop')" :disabled="current.state !== 'running'">停止</el-button>
          <el-button size="small" @click="act(current!, 'restart')">重启</el-button>
        </div>

        <el-tabs v-model="detailTab" style="flex: 1; min-height: 0">
          <el-tab-pane label="概览" name="info">
            <div class="grid" style="grid-template-columns: repeat(4, 1fr); margin-bottom: 12px">
              <div class="metric">
                <div class="metric-head"><span>CPU</span></div>
                <div class="metric-value" :title="statErr">
                  <template v-if="statErr">-</template>
                  <template v-else>{{ num(stat.cpu_percent, 2) }}<small>%</small></template>
                </div>
              </div>
              <div class="metric">
                <div class="metric-head"><span>内存</span></div>
                <div class="metric-value" :title="statErr">{{ statErr ? '-' : fmtBytes(stat.mem_usage) }}</div>
                <div class="metric-foot">{{ statErr ? '暂不可用' : '限制 ' + fmtBytes(stat.mem_limit) }}</div>
              </div>
              <div class="metric">
                <div class="metric-head"><span>网络 I/O</span></div>
                <div class="metric-value" style="font-size: 17px">↓{{ fmtBytes(stat.net_rx) }}</div>
                <div class="metric-foot">↑{{ fmtBytes(stat.net_tx) }}</div>
              </div>
              <div class="metric">
                <div class="metric-head"><span>进程数</span></div>
                <div class="metric-value">{{ stat.pids || 0 }}</div>
                <div class="metric-foot">块 I/O {{ fmtBytes((stat.blk_read || 0) + (stat.blk_write || 0)) }}</div>
              </div>
            </div>
            <el-descriptions :column="2" border size="small" v-if="inspect">
              <el-descriptions-item label="容器 ID"><span class="mono">{{ inspect.Id?.slice(0, 24) }}</span></el-descriptions-item>
              <el-descriptions-item label="重启策略">{{ inspect.HostConfig?.RestartPolicy?.Name || 'no' }}</el-descriptions-item>
              <el-descriptions-item label="启动时间">{{ fmtTime(inspect.State?.StartedAt) }}</el-descriptions-item>
              <el-descriptions-item label="退出码">
                <span>{{ inspect.State?.ExitCode ?? '-' }}</span>
                <el-tooltip v-if="exitHint" placement="top" effect="dark">
                  <template #content>
                    <div style="max-width: 300px; line-height: 1.7; font-size: 12px">{{ exitHint.long }}</div>
                  </template>
                  <span class="exit-hint">{{ exitHint.short }}</span>
                </el-tooltip>
              </el-descriptions-item>
              <el-descriptions-item label="工作目录">{{ inspect.Config?.WorkingDir || '/' }}</el-descriptions-item>
              <el-descriptions-item label="网络模式">{{ inspect.HostConfig?.NetworkMode || '-' }}</el-descriptions-item>
              <el-descriptions-item label="启动命令" :span="2">
                <span class="mono" style="font-size: 11.5px">
                  {{ (inspect.Config?.Cmd || []).join(' ') || inspect.Path || '-' }}
                </span>
              </el-descriptions-item>
              <el-descriptions-item label="环境变量" :span="2">
                <div class="mono" style="font-size: 11px; max-height: 90px; overflow: auto">
                  <div v-for="e in inspect.Config?.Env || []" :key="e">{{ e }}</div>
                </div>
              </el-descriptions-item>
              <el-descriptions-item label="挂载" :span="2">
                <div class="mono" style="font-size: 11px">
                  <div v-for="m in inspect.Mounts || []" :key="m.Destination">{{ m.Source }} → {{ m.Destination }}</div>
                  <span v-if="!inspect.Mounts?.length" class="muted">无</span>
                </div>
              </el-descriptions-item>
            </el-descriptions>
          </el-tab-pane>

          <el-tab-pane label="日志" name="logs">
            <div class="row" style="margin-bottom: 8px">
              <el-radio-group v-model="logTail" size="small" @change="restartLogs">
                <el-radio-button label="100">100 行</el-radio-button>
                <el-radio-button label="500">500 行</el-radio-button>
                <el-radio-button label="2000">2000 行</el-radio-button>
              </el-radio-group>
              <div class="spacer"></div>
              <el-button size="small" @click="restartLogs">重新连接</el-button>
            </div>
            <pre ref="logBox" class="log-box">{{ logs }}</pre>
          </el-tab-pane>

          <el-tab-pane label="终端" name="exec">
            <div style="height: calc(100vh - 240px)">
              <XTerm v-if="detailTab === 'exec'" :key="execKey" :url="execUrl" :title="current.name" />
            </div>
          </el-tab-pane>
        </el-tabs>
      </div>
    </el-drawer>

    <!-- 新建容器 -->
    <el-dialog v-model="createVisible" title="新建容器" width="660px" top="6vh">
      <el-form :model="form" label-width="88px" label-position="right">
        <el-form-item label="容器名称"><el-input v-model="form.name" placeholder="可选，留空自动生成" /></el-form-item>
        <el-form-item label="镜像" required>
          <el-input v-model="form.image" placeholder="nginx:latest" />
        </el-form-item>
        <el-form-item label="启动命令"><el-input v-model="form.cmd" placeholder="可选，例如 nginx -g 'daemon off;'" /></el-form-item>

        <el-form-item label="端口映射">
          <div style="width: 100%">
            <div v-for="(p, i) in form.ports" :key="i" class="row" style="margin-bottom: 6px">
              <el-input v-model.number="p.host_port" placeholder="宿主端口" style="width: 120px" />
              <span class="muted">→</span>
              <el-input v-model.number="p.container_port" placeholder="容器端口" style="width: 120px" />
              <el-select v-model="p.proto" style="width: 88px">
                <el-option label="tcp" value="tcp" />
                <el-option label="udp" value="udp" />
              </el-select>
              <el-button link type="danger" @click="form.ports.splice(i, 1)">移除</el-button>
            </div>
            <el-button size="small" @click="form.ports.push({ host_port: undefined, container_port: undefined, proto: 'tcp' })">
              + 添加端口
            </el-button>
          </div>
        </el-form-item>

        <el-form-item label="目录挂载">
          <div style="width: 100%">
            <div v-for="(b, i) in form.binds" :key="i" class="row" style="margin-bottom: 6px">
              <el-input v-model="b.host_path" placeholder="/data/app" style="width: 200px" />
              <span class="muted">:</span>
              <el-input v-model="b.container_path" placeholder="/app" style="width: 190px" />
              <el-checkbox v-model="b.read_only">只读</el-checkbox>
              <el-button link type="danger" @click="form.binds.splice(i, 1)">移除</el-button>
            </div>
            <el-button size="small" @click="form.binds.push({ host_path: '', container_path: '', read_only: false })">
              + 添加挂载
            </el-button>
          </div>
        </el-form-item>

        <el-form-item label="环境变量">
          <el-input v-model="envText" type="textarea" :rows="3" placeholder="每行一个，KEY=VALUE" />
        </el-form-item>

        <el-form-item label="重启策略">
          <el-select v-model="form.restart_policy" style="width: 200px">
            <el-option label="不自动重启" value="no" />
            <el-option label="总是重启" value="always" />
            <el-option label="异常退出时重启" value="on-failure" />
            <el-option label="除非手动停止" value="unless-stopped" />
          </el-select>
          <el-checkbox v-model="form.auto_pull" style="margin-left: 14px">先拉取镜像</el-checkbox>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createVisible = false">取消</el-button>
        <el-button type="primary" :loading="creating" @click="submitCreate">创建并启动</el-button>
      </template>
    </el-dialog>

    <!-- 拉取镜像 -->
    <el-dialog v-model="pullVisible" title="拉取镜像" width="560px">
      <el-input v-model="pullRef" placeholder="例如 redis:7-alpine" @keyup.enter="doPull" />
      <pre v-if="pullLog" class="log-box" style="height: 220px; margin-top: 12px">{{ pullLog }}</pre>
      <template #footer>
        <el-button @click="pullVisible = false">关闭</el-button>
        <el-button type="primary" :loading="pulling" @click="doPull">开始拉取</el-button>
      </template>
    </el-dialog>

    <!-- 编辑 docker-compose -->
    <el-dialog v-model="composeVisible" :title="'编辑 Compose · ' + composeProject" width="820px" top="4vh">
      <div class="row" style="margin-bottom: 8px; align-items: center">
        <span class="muted mono" style="font-size: 12px; word-break: break-all">{{ composePath }}</span>
        <div class="spacer"></div>
        <el-button size="small" :disabled="composeBusy" @click="loadCompose">重新加载</el-button>
      </div>
      <el-input
        v-model="composeContent"
        type="textarea"
        class="compose-editor mono"
        :rows="20"
        resize="vertical"
        spellcheck="false"
      />
      <div class="muted" style="font-size: 11.5px; margin-top: 8px; line-height: 1.7">
        保存时原文件会自动备份为 <span class="mono">*.bak</span>；「应用」在堆栈目录执行
        <span class="mono">docker compose up -d --remove-orphans</span>，已从配置中移除的服务容器会被清理。
      </div>
      <pre v-if="composeLog" class="log-box" style="height: 200px; margin-top: 10px">{{ composeLog }}</pre>
      <template #footer>
        <el-button @click="composeVisible = false">关闭</el-button>
        <el-button :disabled="composeBusy" @click="saveCompose(false)">仅保存</el-button>
        <el-button type="primary" :loading="composeBusy" @click="saveCompose(true)">保存并应用</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Refresh, Plus, Search, Download, WarningFilled, CaretRight, EditPen } from '@element-plus/icons-vue'
import XTerm from '../components/XTerm.vue'
import { api, errMsg, getToken, hostQuery, wsURL } from '../api'
import { fmtBytes, fmtTime, num, percentClass } from '../utils'

const items = ref<any[]>([])
const images = ref<any[]>([])
const networks = ref<any[]>([])
const volumes = ref<any[]>([])
const loading = ref(false)
const dockerErr = ref('')
const q = ref('')
// 容器状态筛选：all 全部 / running 运行中 / stopped 已停止（非 running 都算）
const stateFilter = ref<'all' | 'running' | 'stopped'>('all')
const tab = ref('containers')

const drawer = ref(false)
const current = ref<any>(null)
const inspect = ref<any>(null)
const stat = ref<any>({})
const statErr = ref('')
const detailTab = ref('info')
const execKey = ref(0)
const logTail = ref('200')
const logs = ref('')
const logBox = ref<HTMLElement>()

const createVisible = ref(false)
const creating = ref(false)
const envText = ref('')
const form = reactive<any>({
  name: '',
  image: '',
  cmd: '',
  ports: [],
  binds: [],
  restart_policy: 'unless-stopped',
  auto_pull: true
})

const pullVisible = ref(false)
const pulling = ref(false)
const pullRef = ref('')
const pullLog = ref('')

let statsTimer: any = null
let listTimer: any = null
let inflight = false
const statsLoading = ref(false)
let logWS: WebSocket | null = null
let searchTimer: any = null

const execUrl = computed(() =>
  current.value ? wsURL(`/ws/containers/${current.value.id}/exec`, { shell: '/bin/sh' }) : ''
)

function stateType(s: string) {
  if (s === 'running') return 'success'
  if (s === 'paused') return 'warning'
  if (s === 'restarting') return 'warning'
  if (s === 'exited' || s === 'dead') return 'danger'
  return 'info'
}
function stateText(s: string) {
  const map: Record<string, string> = {
    running: '运行中',
    exited: '已停止',
    paused: '已暂停',
    created: '已创建',
    restarting: '重启中',
    dead: '异常',
    removing: '删除中'
  }
  return map[s] || s
}

// ---------- 退出码解释 ----------
// 常见约定：0 正常；1 应用错误；125/126/127 与命令执行有关；
// 128+信号编号 为信号退出（137=SIGKILL、143=SIGTERM 最常见）。
const exitHint = computed<{ short: string; long: string } | null>(() => {
  const st = inspect.value?.State
  if (!st || st.Running || (st.ExitCode ?? null) === null) return null
  const code = Number(st.ExitCode)
  const known: Record<number, { short: string; long: string }> = {
    0: { short: '正常退出', long: '退出码 0：容器内进程正常执行结束。' },
    1: { short: '应用错误', long: '退出码 1：进程发生一般性错误退出，建议查看「日志」定位具体报错。' },
    2: { short: '命令用法错误', long: '退出码 2：shell 内建命令或参数用法错误（bash 误用时返回）。' },
    125: { short: '启动失败', long: '退出码 125：Docker 自身执行容器出错，常见于命令不存在、端口冲突或权限不足。' },
    126: { short: '不可执行', long: '退出码 126：命令存在但没有执行权限（缺少 +x 或非可执行文件）。' },
    127: { short: '命令未找到', long: '退出码 127：容器内找不到指定的命令或脚本路径，检查镜像内容器和启动命令拼写。' },
    130: { short: 'Ctrl+C 中断', long: '退出码 130：进程收到 SIGINT（128+2）被中断，通常是交互式终端里按了 Ctrl+C。' },
    137: {
      short: st.OOMKilled ? '内存超限 (OOM)' : '被强制杀死',
      long: st.OOMKilled
        ? '退出码 137（128+9，SIGKILL）：容器内存用量超过限制被内核强制终止（OOMKilled），可调大内存限制或排查泄漏。'
        : '退出码 137（128+9，SIGKILL）：进程被强制杀死，常见于 docker stop 等待超时后的强杀，或被系统 OOM / kill -9 终止。'
    },
    139: { short: '段错误崩溃', long: '退出码 139（128+11，SIGSEGV）：进程段错误崩溃，通常是应用程序自身的 bug。' },
    143: { short: '正常停止 (SIGTERM)', long: '退出码 143（128+15，SIGTERM）：进程收到终止信号退出，docker stop / compose stop 的正常停止流程即为此码。' },
    255: { short: '异常退出', long: '退出码 255：应用以越界退出码退出，常见于入口脚本执行失败或程序异常。' }
  }
  if (known[code]) return known[code]
  if (code >= 128) {
    const sig = code - 128
    return {
      short: `信号 ${sig} 退出`,
      long: `退出码 ${code}（128+${sig}）：进程收到编号为 ${sig} 的信号被终止。`
    }
  }
  return {
    short: '自定义退出码',
    long: `退出码 ${code}：由应用自行定义，具体含义需参考该程序的文档或日志。`
  }
})

// 运行中的容器一定占用内存，所以 mem_usage 为 0 只可能是「没采集到」，
// 这时显示「-」而不是 0，避免看起来像真的 0%。
function statsKnown(row: any) {
  return row.state === 'running' && (num(row.mem_usage) > 0 || num(row.cpu_percent) > 0)
}

function statsTip(row: any) {
  if (row.stats_error) return '资源采集失败：' + row.stats_error
  return '未获取到资源占用数据：目标主机上的 Warden 可能版本不一致（被管端需与主控同版本），或该主机 Docker 未开放 stats 接口'
}

// 所有运行中的容器都没有数据时，多半是被管端版本过旧，直接给一条提示
const statsUnavailable = computed(() => {
  if (statsLoading.value) return false
  const running = items.value.filter((r: any) => r.state === 'running')
  return running.length > 0 && running.every((r: any) => !statsKnown(r))
})

const statsUnavailableReason = computed(() => {
  const err = items.value.find((r: any) => r.state === 'running' && r.stats_error)?.stats_error
  if (err) return `采集失败：${err}`
  return '当前主机的被管端没有返回 CPU / 内存数据。常见原因：目标主机上的 Warden 版本与控制端不一致（需同为最新版），或该主机 Docker 的 stats 接口不可用。'
})

// ---------- 按 compose 堆栈分组 ----------

// 状态筛选后的容器列表；「已停止」= 非 running（含 exited / created / dead / paused）
const filteredItems = computed(() => {
  if (stateFilter.value === 'running') return items.value.filter((r: any) => r.state === 'running')
  if (stateFilter.value === 'stopped') return items.value.filter((r: any) => r.state !== 'running')
  return items.value
})

const emptyText = computed(() =>
  stateFilter.value === 'all' ? '没有容器，点击右上角「新建容器」创建' : '没有符合筛选条件的容器'
)

// 有 com.docker.compose.project 标签的容器归入对应堆栈，其余归入「独立容器」。
// 堆栈在工作目录标签里带出文件位置，方便确认编辑的是哪份 compose 文件。
const groupedList = computed(() => {
  const map = new Map<string, any[]>()
  for (const r of filteredItems.value) {
    const key = r.project || ''
    if (!map.has(key)) map.set(key, [])
    map.get(key)!.push(r)
  }
  const stacks = [...map.entries()]
    .filter(([k]) => k)
    .sort((a, b) => a[0].localeCompare(b[0]))
    .map(([k, containers]) => ({
      key: 's:' + k,
      stack: k,
      title: k,
      dir: containers[0]?.labels?.['com.docker.compose.project.working_dir'] || '',
      running: containers.filter((r: any) => r.state === 'running').length,
      containers
    }))
  const loose = map.get('') || []
  if (loose.length || stacks.length === 0) {
    stacks.push({ key: 'loose', stack: '', title: '独立容器', dir: '', running: loose.filter((r: any) => r.state === 'running').length, containers: loose })
  }
  return stacks
})

const collapsedGroups = reactive<Record<string, boolean>>({})

function toggleGroup(key: string) {
  collapsedGroups[key] = !collapsedGroups[key]
}

// ---------- compose 编辑 ----------

const composeVisible = ref(false)
const composeProject = ref('')
const composePath = ref('')
const composeContent = ref('')
const composeLog = ref('')
const composeBusy = ref(false)

async function openCompose(project: string) {
  composeProject.value = project
  composePath.value = ''
  composeContent.value = ''
  composeLog.value = ''
  composeVisible.value = true
  await loadCompose()
}

async function loadCompose() {
  try {
    const d = await api<any>('/api/compose/file', { query: { project: composeProject.value } })
    composePath.value = d.path
    composeContent.value = d.content
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function saveCompose(apply: boolean) {
  if (composeBusy.value) return
  composeBusy.value = true
  try {
    await api('/api/compose/file', {
      method: 'PUT',
      body: { project: composeProject.value, content: composeContent.value }
    })
    ElMessage.success('已保存')
    if (!apply) return
    composeLog.value = '正在应用配置（docker compose up -d），拉取镜像时可能较慢…\n'
    const r = await api<any>('/api/compose/up', { method: 'POST', body: { project: composeProject.value } })
    composeLog.value = (r.output || '').trim() || (r.ok ? '应用完成' : '应用失败')
    if (r.ok) {
      ElMessage.success('配置已应用')
      await load()
    } else {
      ElMessage.error('应用失败，请查看输出日志')
    }
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    composeBusy.value = false
  }
}

async function fetchList(silent: boolean) {
  // 采集资源占用本身有 1s 左右的耗时，轮询与手动刷新叠在一起会让请求排队
  if (silent && inflight) return
  inflight = true
  if (!silent) loading.value = true
  dockerErr.value = ''
  try {
    const st = await api<any>('/api/docker/status')
    if (!st.available) {
      // Docker 不可用时不必再请求容器列表，避免无意义的报错
      dockerErr.value = st.error || '无法连接 Docker'
      items.value = []
      return
    }
    // 列表本身只查容器，资源占用随后单独异步补齐，保证列表先出来
    // 状态筛选在前端做：已停止的容器只有 all=true 才拿得到，干脆统一取全量
    const res = await api<any>('/api/containers', { query: { all: true, q: q.value } })
    const next: any[] = res.items || []
    // 刷新时沿用上一次的占用值，避免每次轮询都闪一下「-」
    const prev = new Map(items.value.map((r: any) => [r.id, r]))
    for (const r of next) {
      const old = prev.get(r.id)
      if (!old) continue
      r.cpu_percent = old.cpu_percent
      r.mem_usage = old.mem_usage
      r.mem_limit = old.mem_limit
      r.mem_percent = old.mem_percent
      r.stats_error = old.stats_error
    }
    items.value = next
    void fetchStats()
  } catch (e: any) {
    if (silent) return
    dockerErr.value = errMsg(e)
    items.value = []
  } finally {
    inflight = false
    loading.value = false
  }
}

// 容器渲染完成后再补 CPU / 内存：采集要等两帧（约 1s），不该挡住列表
async function fetchStats() {
  if (!items.value.some((r: any) => r.state === 'running')) {
    statsLoading.value = false
    return
  }
  statsLoading.value = true
  try {
    const res = await api<any>('/api/containers/stats')
    const map = new Map<string, any>((res?.items || []).map((s: any) => [s.id, s]))
    for (const row of items.value) {
      const s = map.get(row.id)
      if (!s) continue
      row.cpu_percent = s.cpu_percent
      row.mem_usage = s.mem_usage
      row.mem_limit = s.mem_limit
      row.mem_percent = s.mem_percent
      row.stats_error = s.stats_error
    }
  } catch {
    // 资源数据是锦上添花，失败不该打断列表
  } finally {
    statsLoading.value = false
  }
}

function load() {
  return fetchList(false)
}

// 资源占用是实时值，静默轮询刷新；弹层打开或切到其他标签页时暂停，避免打扰
function refreshQuiet() {
  if (tab.value !== 'containers' || drawer.value || createVisible.value || composeVisible.value || loading.value) return
  if (document.hidden) return
  fetchList(true)
}

async function loadAll() {
  await load()
  if (tab.value === 'images') {
    try {
      images.value = (await api<any>('/api/images')).items || []
    } catch {}
  }
  if (tab.value === 'networks') {
    try {
      networks.value = (await api<any>('/api/networks')).items || []
      volumes.value = (await api<any>('/api/volumes')).items || []
    } catch {}
  }
}

function onSearch() {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(load, 300)
}

watch(tab, loadAll)

async function act(row: any, action: string) {
  try {
    await api(`/api/containers/${row.id}/${action}`, { method: 'POST' })
    ElMessage.success('操作成功')
    await load()
    if (current.value?.id === row.id) await loadDetail(row.id)
  } catch (e) {
    ElMessage.error(errMsg(e))
    // 报错多半是列表过期（容器已被删除等），静默刷新掉过期行
    fetchList(true)
  }
}

async function remove(row: any) {
  try {
    await ElMessageBox.confirm(
      `确定删除容器「${row.name || row.id.slice(0, 12)}」吗？该操作不可恢复。`,
      '删除容器',
      { type: 'warning', confirmButtonText: '删除', confirmButtonClass: 'el-button--danger' }
    )
  } catch {
    return
  }
  try {
    await api(`/api/containers/${row.id}`, { method: 'DELETE', query: { force: true } })
    ElMessage.success('已删除')
    drawer.value = false
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
    fetchList(true)
  }
}

async function loadDetail(id: string) {
  try {
    inspect.value = await api<any>(`/api/containers/${id}`)
  } catch {
    inspect.value = null
  }
  try {
    stat.value = (await api<any>(`/api/containers/${id}/stats`)) || {}
    statErr.value = ''
  } catch (e) {
    // 容器可能已停止，或该主机的 stats 接口不可用
    stat.value = {}
    statErr.value = errMsg(e)
  }
}

function openDetail(row: any) {
  current.value = row
  drawer.value = true
  detailTab.value = 'info'
  execKey.value++
  logs.value = ''
  loadDetail(row.id)
  restartLogs()
  if (statsTimer) clearInterval(statsTimer)
  statsTimer = setInterval(() => {
    if (drawer.value && current.value && detailTab.value === 'info') loadDetail(current.value.id)
  }, 2000)
}

function restartLogs() {
  if (logWS) {
    logWS.onclose = null
    logWS.close()
  }
  logs.value = ''
  if (!current.value) return
  logWS = new WebSocket(wsURL(`/ws/containers/${current.value.id}/logs`, { tail: logTail.value }))
  logWS.binaryType = 'arraybuffer'
  logWS.onmessage = (ev) => {
    let text = typeof ev.data === 'string' ? ev.data : new TextDecoder().decode(ev.data)
    if (text.startsWith('\u0000NOTICE:')) {
      ElMessage.error(text.slice(8))
      return
    }
    logs.value += text
    if (logs.value.length > 400000) logs.value = logs.value.slice(-300000)
    nextTick(() => {
      if (logBox.value) logBox.value.scrollTop = logBox.value.scrollHeight
    })
  }
  logWS.onclose = () => {
    if (drawer.value && detailTab.value === 'logs') logs.value += '\n[连接已断开]'
  }
}

watch(detailTab, (v) => {
  if (v === 'logs') restartLogs()
  if (v === 'exec') execKey.value++
})

function openCreate() {
  form.name = ''
  form.image = ''
  form.cmd = ''
  form.ports = [{ host_port: undefined, container_port: undefined, proto: 'tcp' }]
  form.binds = []
  form.restart_policy = 'unless-stopped'
  form.auto_pull = true
  envText.value = ''
  createVisible.value = true
}

async function submitCreate() {
  if (!form.image) return ElMessage.warning('请填写镜像')
  creating.value = true
  try {
    await api('/api/containers', {
      body: {
        ...form,
        ports: form.ports.filter((p: any) => p.container_port),
        binds: form.binds.filter((b: any) => b.host_path && b.container_path),
        env: envText.value
          .split('\n')
          .map((s) => s.trim())
          .filter(Boolean)
      }
    })
    ElMessage.success('容器已创建')
    createVisible.value = false
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    creating.value = false
  }
}

async function doPull() {
  if (!pullRef.value) return
  pulling.value = true
  pullLog.value = ''
  try {
    const res = await fetch('/api/images/pull?' + hostQuery(), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${getToken()}` },
      body: JSON.stringify({ image: pullRef.value })
    })
    if (!res.body) throw new Error('无法读取进度流')
    const reader = res.body.getReader()
    const dec = new TextDecoder()
    let buf = ''
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      buf += dec.decode(value, { stream: true })
      const parts = buf.split('\n\n')
      buf = parts.pop() || ''
      for (const part of parts) {
        const line = part.split('\n').find((l) => l.startsWith('data:'))
        if (!line) continue
        try {
          const j = JSON.parse(line.slice(5).trim())
          if (j.status) pullLog.value += (j.id ? j.id + ' ' : '') + j.status + (j.detail ? ' ' + j.detail : '') + '\n'
          if (j.message) pullLog.value += j.message + '\n'
        } catch {}
      }
      if (pullLog.value.length > 20000) pullLog.value = pullLog.value.slice(-15000)
    }
    ElMessage.success('拉取完成')
    pullVisible.value = false
    loadAll()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    pulling.value = false
  }
}

async function removeImage(row: any) {
  try {
    await ElMessageBox.confirm(`删除镜像 ${row.tags?.[0]}？`, '删除镜像', { type: 'warning' })
  } catch {
    return
  }
  try {
    await api(`/api/images/${encodeURIComponent(row.id)}`, { method: 'DELETE', query: { force: true } })
    ElMessage.success('已删除')
    loadAll()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

onMounted(() => {
  loadAll()
  if (listTimer) clearInterval(listTimer)
  listTimer = setInterval(refreshQuiet, 5000)
})

onBeforeUnmount(() => {
  if (listTimer) clearInterval(listTimer)
  if (statsTimer) clearInterval(statsTimer)
  if (logWS) {
    logWS.onclose = null
    logWS.close()
  }
})
</script>

<style scoped>
.exit-hint {
  margin-left: 6px;
  font-size: 11.5px;
  color: var(--ink-3);
  border-bottom: 1px dashed var(--ink-3);
  cursor: help;
}
.stack-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--line);
  background: #f8fafd;
  cursor: pointer;
  user-select: none;
}
.stack-chevron {
  color: var(--ink-3);
  transition: transform 0.15s;
}
.stack-chevron.collapsed {
  transform: rotate(0deg);
}
.stack-chevron:not(.collapsed) {
  transform: rotate(90deg);
}
.stack-name {
  font-weight: 600;
  font-size: 13.5px;
  color: var(--ink);
}
.stack-name.loose {
  font-weight: 500;
  color: var(--ink-2);
}
.stack-dir {
  font-size: 11px;
  max-width: 340px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.compose-editor :deep(textarea) {
  font-family: 'SFMono-Regular', Consolas, Menlo, monospace;
  font-size: 12.5px;
  line-height: 1.6;
  background: #fbfcfe;
}
.cell-metric {
  font-size: 12px;
  line-height: 1.35;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.cell-metric .progress {
  height: 3px;
  margin-top: 5px;
}
.state-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: #b4b2a9;
  flex: 0 0 auto;
  margin-right: 2px;
}
.state-dot.running {
  background: #2ba471;
  box-shadow: 0 0 0 3px rgba(43, 164, 113, 0.14);
}
.state-dot.exited,
.state-dot.dead {
  background: #c2382f;
}
.state-dot.paused {
  background: #e6a23c;
}
.port-chip {
  display: inline-block;
  font-size: 11px;
  padding: 1px 6px;
  margin-right: 4px;
  background: #f2f4f9;
  border-radius: 4px;
  color: #5a6478;
}
.log-box {
  background: #101725;
  color: #cdd6e6;
  border-radius: 8px;
  padding: 12px 14px;
  height: calc(100vh - 260px);
  overflow: auto;
  font-family: 'SFMono-Regular', Consolas, Menlo, monospace;
  font-size: 12px;
  line-height: 1.65;
  margin: 0;
  white-space: pre-wrap;
  word-break: break-all;
}
</style>
