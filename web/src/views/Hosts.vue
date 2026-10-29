<template>
  <div class="page">
    <div class="card head-card">
      <div class="head-left">
        <div class="head-title">主机列表</div>
        <div class="head-desc">
          控制端通过 Agent 纳管多台服务器，切换主机后所有页面都会跟随目标主机
        </div>
      </div>
      <div class="row" style="gap: 8px">
        <el-button :icon="Refresh" :loading="loading" @click="reload">刷新</el-button>
        <el-button type="primary" :icon="Plus" @click="openCreate">添加主机</el-button>
      </div>
    </div>

    <div class="stat-strip">
      <div class="stat">
        <span>主机总数</span>
        <b>{{ hostList.length }}</b>
      </div>
      <div class="stat">
        <span>在线</span>
        <b class="ok">{{ onlineCount }}</b>
      </div>
      <div class="stat">
        <span>离线</span>
        <b :class="{ bad: offlineCount > 0 }">{{ offlineCount }}</b>
      </div>
      <div class="stat">
        <span>当前视角</span>
        <b style="font-size: 14px">{{ activeHost?.name || '-' }}</b>
      </div>
    </div>

    <div class="card" style="padding: 0">
      <el-table
        :data="hostList"
        v-loading="loading"
        size="default"
        empty-text="暂无主机"
        row-key="id"
      >
        <el-table-column label="主机" min-width="210">
          <template #default="{ row }">
            <div class="host-cell">
              <span class="host-dot" :class="row.online ? 'on' : 'off'"></span>
              <div class="host-main">
                <div class="host-title">
                  {{ row.name }}
                  <el-tag v-if="row.kind === 'local'" size="small" type="info" effect="plain">
                    控制端
                  </el-tag>
                  <el-tag
                    v-else-if="row.id === activeHostId"
                    size="small"
                    type="primary"
                    effect="plain"
                  >
                    当前
                  </el-tag>
                  <el-tag v-if="!row.enabled" size="small" type="warning" effect="plain">
                    已停用
                  </el-tag>
                </div>
                <div class="host-sub">
                  {{ row.remark || row.hostname || '—' }}
                  <span v-if="row.group_name" class="group-chip">{{ row.group_name }}</span>
                </div>
              </div>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="地址" width="172">
          <template #default="{ row }">
            <span class="mono">{{ row.kind === 'local' ? 'localhost' : row.address }}</span>
          </template>
        </el-table-column>

        <el-table-column label="状态" width="118">
          <template #default="{ row }">
            <template v-if="row.kind === 'local'">
              <span class="state ok">本机</span>
            </template>
            <template v-else-if="row.online">
              <span class="state ok">在线</span>
              <span class="muted" style="margin-left: 6px">{{ row.latency_ms }}ms</span>
            </template>
            <template v-else>
              <el-tooltip :content="row.error || '未探测'" placement="top">
                <span class="state bad">离线</span>
              </el-tooltip>
            </template>
          </template>
        </el-table-column>

        <el-table-column label="系统" min-width="230">
          <template #default="{ row }">
            <div v-if="row.platform" class="sys-cell">
              <div class="sys-name">{{ row.platform }}</div>
              <div class="muted">
                {{ row.arch }} · {{ row.cpu_cores }} 核 · {{ gb(row.total_mem) }} 内存
                <span v-if="row.version" class="ver-chip">v{{ row.version }}</span>
              </div>
            </div>
            <span v-else class="muted">—</span>
          </template>
        </el-table-column>

        <el-table-column label="Docker" width="86">
          <template #default="{ row }">
            <span v-if="row.docker_ok" class="state ok">可用</span>
            <span v-else class="muted">—</span>
          </template>
        </el-table-column>

        <el-table-column label="操作" width="228" fixed="right">
          <template #default="{ row }">
            <el-button
              v-if="row.id !== activeHostId"
              link
              type="primary"
              size="small"
              @click="useHost(row)"
            >
              切换
            </el-button>
            <el-button
              v-if="row.kind !== 'local'"
              link
              type="primary"
              size="small"
              :loading="testingId === row.id"
              @click="probe(row)"
            >
              探测
            </el-button>
            <el-button link size="small" @click="openEdit(row)">编辑</el-button>
            <el-dropdown v-if="row.kind !== 'local'" trigger="click" @command="(c: string) => onMore(c, row)" style="margin-left: 10px;vertical-align: middle;">
              <el-button link size="small">更多</el-button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item command="token">重置令牌</el-dropdown-item>
                  <el-dropdown-item command="delete" divided>删除主机</el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 新增/编辑 -->
    <el-dialog v-model="formVisible" :title="editing ? '编辑主机' : '添加主机'" width="520px">
      <el-form :model="form" label-width="92px" style="padding-right: 8px">
        <el-form-item label="主机名称" required>
          <el-input v-model="form.name" placeholder="例如 生产环境-01" maxlength="64" />
        </el-form-item>
        <template v-if="!isLocalEdit">
          <el-form-item label="Agent 地址" required>
            <el-input v-model="form.address" placeholder="10.0.0.12:8090" />
            <div class="hint">Agent 实际监听的地址与端口，默认 8090</div>
          </el-form-item>
          <el-form-item label="分组">
            <el-input v-model="form.group_name" placeholder="可选，例如 生产" maxlength="32" />
          </el-form-item>
          <el-form-item label="TLS">
            <div class="row" style="gap: 16px">
              <el-switch v-model="form.tls" active-text="Agent 使用 HTTPS" />
              <el-checkbox v-model="form.insecure" :disabled="!form.tls">跳过证书校验</el-checkbox>
            </div>
          </el-form-item>
          <el-form-item label="启用">
            <el-switch v-model="form.enabled" />
          </el-form-item>
        </template>
        <el-form-item label="备注">
          <el-input v-model="form.remark" type="textarea" :rows="2" maxlength="120" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="formVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">保存</el-button>
      </template>
    </el-dialog>

    <!-- 令牌与安装命令 -->
    <el-dialog v-model="tokenVisible" :title="tokenTitle" width="640px">
      <el-alert type="warning" :closable="false" show-icon style="margin-bottom: 14px">
        <template #title>令牌只显示这一次，请立即配置到目标机</template>
      </el-alert>

      <div class="kv">
        <div class="kv-label">访问令牌</div>
        <div class="kv-body">
          <code class="cmd">{{ tokenValue }}</code>
          <el-button link type="primary" size="small" @click="copy(tokenValue)">复制</el-button>
        </div>
      </div>

      <div class="kv">
        <div class="kv-label">启动命令</div>
        <div class="kv-body">
          <code class="cmd">{{ installCmd }}</code>
          <el-button link type="primary" size="small" @click="copy(installCmd)">复制</el-button>
        </div>
      </div>

      <div class="kv">
        <div class="kv-label">写入配置</div>
        <div class="kv-body">
          <pre class="cmd pre">{{ configSnippet }}</pre>
          <el-button link type="primary" size="small" @click="copy(configSnippet)">复制</el-button>
        </div>
      </div>

      <div class="tips">
        目标机同样使用本程序（无需安装 Docker SDK 之类依赖）：把二进制传到目标机后执行上面的命令，
        或用 <code>install-agent.sh</code> 装成 systemd 服务。Agent 默认监听 8090，
        防火墙需放行控制端到该端口的访问。
      </div>

      <template #footer>
        <el-button type="primary" @click="tokenVisible = false">我已配置好</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh } from '@element-plus/icons-vue'
import { api, errMsg } from '../api'
import { copyText } from '../utils'
import { activeHost, activeHostId, hostList, loadHosts, onlineCount, switchHost } from '../hosts'

const loading = ref(false)
const saving = ref(false)
const testingId = ref(0)

const formVisible = ref(false)
const editing = ref(false)
const isLocalEdit = ref(false)

const tokenVisible = ref(false)
const tokenTitle = ref('主机接入信息')
const tokenValue = ref('')
const installCmd = ref('')
const configSnippet = ref('')

const form = reactive<any>({
  id: 0,
  name: '',
  address: '',
  group_name: '',
  remark: '',
  tls: false,
  insecure: false,
  enabled: true
})

const offlineCount = computed(() => hostList.value.filter((h) => !h.online).length)

function gb(n: number) {
  if (!n) return '—'
  return (n / 1024 / 1024 / 1024).toFixed(1) + ' G'
}

async function reload() {
  loading.value = true
  try {
    await loadHosts()
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editing.value = false
  isLocalEdit.value = false
  Object.assign(form, {
    id: 0,
    name: '',
    address: '',
    group_name: '',
    remark: '',
    tls: false,
    insecure: false,
    enabled: true
  })
  formVisible.value = true
}

function openEdit(row: any) {
  editing.value = true
  isLocalEdit.value = row.kind === 'local'
  Object.assign(form, {
    id: row.id,
    name: row.name,
    address: row.address,
    group_name: row.group_name,
    remark: row.remark,
    tls: row.tls,
    insecure: row.insecure,
    enabled: row.enabled
  })
  formVisible.value = true
}

async function save() {
  if (!form.name.trim()) return ElMessage.warning('请填写主机名称')
  if (!isLocalEdit.value && !form.address.trim()) return ElMessage.warning('请填写 Agent 地址')
  saving.value = true
  try {
    const body = {
      name: form.name.trim(),
      address: form.address.trim(),
      group_name: form.group_name.trim(),
      remark: form.remark,
      tls: form.tls,
      insecure: form.insecure,
      enabled: form.enabled
    }
    if (editing.value) {
      await api(`/api/hosts/${form.id}`, { method: 'PUT', body })
      ElMessage.success('已保存')
    } else {
      const res = await api<any>('/api/hosts', { method: 'POST', body })
      ElMessage.success('主机已添加')
      showToken(res.token, res.install_cmd, res.config_snippet, '主机接入信息')
    }
    formVisible.value = false
    await reload()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
  }
}

function showToken(token: string, cmd: string, snippet: string, title: string) {
  tokenValue.value = token
  installCmd.value = cmd
  configSnippet.value = snippet || ''
  tokenTitle.value = title
  tokenVisible.value = true
}

async function probe(row: any) {
  testingId.value = row.id
  try {
    const res = await api<any>(`/api/hosts/${row.id}/test`, { method: 'POST' })
    if (res.online) ElMessage.success(`连接正常，延迟 ${res.status?.latency_ms ?? 0} ms`)
    else ElMessage.error(res.message || '连接失败')
    await reload()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    testingId.value = 0
  }
}

function useHost(row: any) {
  switchHost(row.id)
  ElMessage.success(`已切换到【${row.name}】`)
}

async function onMore(cmd: string, row: any) {
  if (cmd === 'token') return rotate(row)
  if (cmd === 'delete') return remove(row)
}

async function rotate(row: any) {
  await ElMessageBox.confirm(
    `重置后【${row.name}】的旧令牌立即失效，需要同步修改目标机 Agent 配置。是否继续？`,
    '重置访问令牌',
    { type: 'warning' }
  ).catch(() => null)
  try {
    const res = await api<any>(`/api/hosts/${row.id}/token`, { method: 'POST' })
    showToken(res.token, res.install_cmd, '', '新令牌')
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function remove(row: any) {
  const ok = await ElMessageBox.confirm(
    `确定删除主机【${row.name}】吗？本操作只移除控制端的登记，不会影响目标机上的 Agent。`,
    '删除主机',
    { type: 'warning' }
  ).catch(() => null)
  if (!ok) return
  try {
    await api(`/api/hosts/${row.id}`, { method: 'DELETE' })
    ElMessage.success('已删除')
    if (activeHostId.value === row.id) {
      const local = hostList.value.find((h) => h.kind === 'local')
      if (local) switchHost(local.id)
    }
    await reload()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function copy(text: string) {
  if (await copyText(text)) ElMessage.success('已复制')
  else ElMessage.warning('复制失败，请手动选择文本')
}

onMounted(reload)
</script>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.head-card {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.head-title {
  font-size: 15px;
  font-weight: 600;
}
.head-desc {
  font-size: 12px;
  color: var(--ink-3);
  margin-top: 3px;
}
.stat-strip {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 14px;
}
.stat {
  background: #fff;
  border-radius: 11px;
  padding: 13px 16px;
  display: flex;
  flex-direction: column;
  gap: 4px;
  box-shadow: 0 1px 2px rgba(20, 30, 60, 0.05);
}
.stat span {
  font-size: 11.5px;
  color: var(--ink-3);
}
.stat b {
  font-size: 20px;
  font-weight: 600;
  color: var(--ink);
}
.stat b.ok {
  color: #1f9d5f;
}
.stat b.bad {
  color: #d9534f;
}
.host-cell {
  display: flex;
  gap: 9px;
  align-items: flex-start;
}
.host-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  margin-top: 7px;
  flex: none;
  background: #c3c8d4;
}
.host-dot.on {
  background: #25b26a;
  box-shadow: 0 0 0 3px rgba(37, 178, 106, 0.13);
}
.host-dot.off {
  background: #e05b5b;
  box-shadow: 0 0 0 3px rgba(224, 91, 91, 0.13);
}
.host-title {
  font-weight: 500;
  display: flex;
  align-items: center;
  gap: 6px;
}
.host-sub {
  font-size: 11.5px;
  color: var(--ink-3);
  margin-top: 2px;
  display: flex;
  align-items: center;
  gap: 6px;
}
.group-chip {
  background: #f0f2f7;
  border-radius: 5px;
  padding: 0 5px;
  font-size: 10.5px;
  color: var(--ink-2);
}
.sys-name {
  font-size: 12.5px;
  color: var(--ink);
}
.ver-chip {
  font-family: ui-monospace, Menlo, Consolas, monospace;
  background: #f2f4fa;
  border-radius: 4px;
  padding: 0 4px;
  margin-left: 4px;
  font-size: 10.5px;
  color: var(--ink-2);
}
.sys-cell div:last-child {
  font-size: 11.5px;
  margin-top: 2px;
}
.state {
  font-size: 12.5px;
}
.state.ok {
  color: #1f9d5f;
}
.state.bad {
  color: #d9534f;
}
.mono {
  font-family: ui-monospace, Menlo, Consolas, monospace;
  font-size: 12px;
}
.hint {
  font-size: 11px;
  color: var(--ink-3);
  line-height: 1.6;
  margin-top: 2px;
}
.kv {
  display: flex;
  gap: 12px;
  padding: 9px 0;
  border-bottom: 1px dashed #eef0f5;
}
.kv:last-of-type {
  border-bottom: none;
}
.kv-label {
  width: 74px;
  flex: none;
  font-size: 12px;
  color: var(--ink-3);
  padding-top: 3px;
}
.kv-body {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: flex-start;
  gap: 8px;
}
.cmd {
  flex: 1;
  background: #f6f7fb;
  border-radius: 7px;
  padding: 7px 9px;
  font-family: ui-monospace, Menlo, Consolas, monospace;
  font-size: 11.5px;
  color: #2c3648;
  word-break: break-all;
  line-height: 1.6;
}
.pre {
  margin: 0;
  white-space: pre-wrap;
}
.tips {
  margin-top: 14px;
  font-size: 11.5px;
  color: var(--ink-3);
  line-height: 1.8;
  background: #f8f9fc;
  border-radius: 8px;
  padding: 10px 12px;
}
.tips code {
  background: #eef1f8;
  padding: 1px 5px;
  border-radius: 4px;
  font-size: 11px;
}
</style>
