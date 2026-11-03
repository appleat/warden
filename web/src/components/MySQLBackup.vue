<template>
  <div>
    <!-- 说明 -->
    <div class="card" style="padding: 12px 16px">
      <div class="row" style="flex-wrap: wrap; gap: 8px">
        <el-icon :size="15" style="color: var(--ink-3)"><Timer /></el-icon>
        <span style="font-size: 13px; font-weight: 600">定时备份</span>
        <span class="muted" style="font-size: 12px">
          按计划把整库或指定表导出成 SQL，文件保存在控制端的
          <code class="mono">{{ dir || '备份目录' }}</code>
        </span>
        <div class="spacer"></div>
        <el-button size="small" :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
        <el-button size="small" type="primary" :icon="Plus" @click="openCreate">新建备份任务</el-button>
      </div>
      <div class="muted" style="font-size: 11.5px; margin-top: 8px; line-height: 1.8">
        本机主机由控制端直接连库导出；远程主机把导出交给该主机的 Agent，因此数据库只监听
        <code class="mono">127.0.0.1</code> 也能备份。备份使用一致性快照，不加全局读锁，不会阻塞业务写入。
      </div>
    </div>

    <!-- 任务列表 -->
    <div class="card" style="padding: 0; margin-top: 12px">
      <el-table :data="tasks" v-loading="loading" size="small" empty-text="还没有备份任务，点右上角新建">
        <el-table-column label="任务" min-width="190">
          <template #default="{ row }">
            <div class="row" style="gap: 6px; min-width: 0">
              <span class="bk-name">{{ row.name }}</span>
              <el-tag v-if="row.last_status === 'running'" size="small" type="warning" effect="plain">备份中</el-tag>
              <el-tag v-else-if="!row.enabled" size="small" type="info" effect="plain">已停用</el-tag>
            </div>
            <div class="row bk-sub">
              <span class="mono">{{ row.schema }}</span>
              <el-tag size="small" effect="plain" :type="row.scope === 'full' ? '' : 'success'">
                {{ row.scope === 'full' ? '整库' : row.table_count + ' 张表' }}
              </el-tag>
              <span v-if="row.scope === 'tables'" class="bk-ellipsis">· {{ (row.tables || []).join(', ') }}</span>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="计划" width="146">
          <template #default="{ row }">
            <div style="font-size: 12.5px">{{ row.schedule }}</div>
            <div class="muted bk-sub">
              {{ row.enabled ? (row.next_run_text ? '下次 ' + row.next_run_text : '待排期') : '已停用，不再执行' }}
            </div>
          </template>
        </el-table-column>

        <el-table-column label="最近一次" min-width="180">
          <template #default="{ row }">
            <div v-if="row.last_run_at" class="row" style="gap: 6px">
              <span class="stat-pill" :class="row.last_status === 'ok' ? 'ok' : row.last_status === 'running' ? 'warn' : 'bad'">
                <span
                  class="dot"
                  :class="row.last_status === 'ok' ? 'green' : row.last_status === 'running' ? 'yellow' : 'red'"
                ></span>
                {{ statusText(row.last_status) }}
              </span>
              <span class="muted" style="font-size: 11.5px">{{ row.last_run_text }}</span>
            </div>
            <div v-else class="muted" style="font-size: 12px">还没跑过</div>
            <div v-if="row.last_status === 'ok'" class="muted bk-sub">
              {{ fmtBytes(row.last_size) }} · {{ fmtNum(row.last_rows) }} 行
            </div>
            <div
              v-else-if="row.last_status === 'failed' && row.last_error"
              class="bk-error"
              :title="row.last_error"
            >
              {{ row.last_error }}
            </div>
          </template>
        </el-table-column>

        <el-table-column label="保留" width="88">
          <template #default="{ row }">
            <div class="muted" style="font-size: 12px">{{ row.keep > 0 ? row.keep + ' 份' : '不限' }}</div>
            <div class="muted bk-sub">{{ row.compress ? 'gzip' : '不压缩' }}</div>
          </template>
        </el-table-column>

        <el-table-column label="启用" width="58">
          <template #default="{ row }">
            <el-switch
              :model-value="row.enabled"
              size="small"
              :disabled="row.last_status === 'running'"
              @change="(v: any) => toggle(row, !!v)"
            />
          </template>
        </el-table-column>

        <el-table-column label="操作" width="204" align="right">
          <template #default="{ row }">
            <el-button
              link
              type="primary"
              size="small"
              :loading="row.last_status === 'running'"
              :disabled="row.last_status === 'running'"
              @click="runNow(row)"
            >立即备份</el-button>
            <el-button link size="small" @click="openFiles(row)">文件</el-button>
            <el-button link size="small" @click="openEdit(row)">编辑</el-button>
            <el-button link type="danger" size="small" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 新建 / 编辑 -->
    <el-dialog
      v-model="dialogVisible"
      :title="form.id ? '编辑备份任务' : '新建备份任务'"
      width="640px"
      :close-on-click-modal="false"
    >
      <el-form label-width="92px">
        <el-form-item label="任务名称">
          <el-input v-model="form.name" maxlength="64" placeholder="例如：订单库每日备份" />
        </el-form-item>

        <el-form-item label="数据库" required>
          <el-select
            v-model="form.schema"
            filterable
            style="width: 100%"
            placeholder="选择要备份的数据库"
            @change="onFormSchemaChange"
          >
            <el-option v-for="d in databases" :key="d.name" :label="d.name" :value="d.name">
              <div class="row" style="justify-content: space-between; gap: 14px">
                <span class="mono">{{ d.name }}</span>
                <span class="muted" style="font-size: 11.5px">
                  {{ d.system ? '系统库' : fmtNum(d.tables) + ' 表' }}
                </span>
              </div>
            </el-option>
          </el-select>
        </el-form-item>

        <el-form-item label="备份范围">
          <el-radio-group v-model="form.scope">
            <el-radio-button label="full">整库</el-radio-button>
            <el-radio-button label="tables">指定表</el-radio-button>
          </el-radio-group>
        </el-form-item>

        <el-form-item v-if="form.scope === 'tables'" label="选择表">
          <div style="width: 100%">
            <el-select
              v-model="form.tables"
              multiple
              filterable
              collapse-tags
              collapse-tags-tooltip
              style="width: 100%"
              :loading="tableLoading"
              placeholder="不选表等于整库"
            >
              <el-option v-for="t in formTables" :key="t.name" :label="t.name" :value="t.name">
                <div class="row" style="justify-content: space-between; gap: 14px">
                  <span class="mono">{{ t.name }}</span>
                  <span class="muted" style="font-size: 11.5px">{{ fmtBytes(t.data_size) }}</span>
                </div>
              </el-option>
            </el-select>
            <div class="muted" style="font-size: 11.5px; margin-top: 4px">
              共 {{ formTables.length }} 张表，已选 {{ form.tables.length }} 张（视图不会备份）
            </div>
          </div>
        </el-form-item>

        <el-form-item label="执行计划">
          <div class="row" style="gap: 8px; flex-wrap: wrap">
            <el-select v-model="form.freq" style="width: 100px">
              <el-option label="每天" value="daily" />
              <el-option label="每周" value="weekly" />
              <el-option label="每月" value="monthly" />
            </el-select>
            <el-select v-if="form.freq === 'weekly'" v-model="form.weekday" style="width: 100px">
              <el-option v-for="(w, i) in weekNames" :key="i" :label="w" :value="i" />
            </el-select>
            <el-select v-if="form.freq === 'monthly'" v-model="form.month_day" style="width: 110px">
              <el-option v-for="d in 28" :key="d" :label="d + ' 日'" :value="d" />
            </el-select>
            <el-time-picker
              v-model="form.time"
              format="HH:mm"
              value-format="HH:mm"
              :clearable="false"
              placeholder="执行时间"
              style="width: 130px"
            />
            <span class="muted" style="font-size: 11.5px">服务器本地时间</span>
          </div>
        </el-form-item>

        <el-form-item label="保留份数">
          <el-input-number v-model="form.keep" :min="0" :max="365" controls-position="right" style="width: 130px" />
          <span class="muted" style="font-size: 11.5px; margin-left: 8px">
            只保留最近 N 份，0 表示不自动清理
          </span>
        </el-form-item>

        <el-form-item label="gzip 压缩">
          <el-switch v-model="form.compress" />
          <span class="muted" style="font-size: 11.5px; margin-left: 8px">文本 SQL 通常能压到 1/5 大小</span>
        </el-form-item>

        <el-form-item label="启用">
          <el-switch v-model="form.enabled" />
          <span class="muted" style="font-size: 11.5px; margin-left: 8px">停用后不再按计划执行，仍可手动备份</span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">保存</el-button>
      </template>
    </el-dialog>

    <!-- 备份文件 -->
    <el-drawer v-model="filesVisible" :title="`备份文件 · ${filesTask?.name || ''}`" size="560px">
      <div class="row" style="gap: 8px; margin-bottom: 10px">
        <span class="muted" style="font-size: 12px">
          {{ files.length }} 个文件 · 共 {{ fmtBytes(filesTotal) }}
        </span>
        <div class="spacer"></div>
        <el-button size="small" :icon="Refresh" :loading="filesLoading" @click="loadFiles">刷新</el-button>
      </div>
      <el-table :data="files" v-loading="filesLoading" size="small" empty-text="还没有备份文件，点「立即备份」生成一份">
        <el-table-column label="文件" min-width="220">
          <template #default="{ row }">
            <span class="mono" style="font-size: 11.5px">{{ row.name }}</span>
          </template>
        </el-table-column>
        <el-table-column label="大小" width="88">
          <template #default="{ row }">
            <span class="muted">{{ fmtBytes(row.size) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="时间" width="140">
          <template #default="{ row }">
            <span class="muted" style="font-size: 12px">{{ fmtTime(row.mod_time) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="110" align="right">
          <template #default="{ row }">
            <el-button link type="primary" size="small" @click="download(row)">下载</el-button>
            <el-button link type="danger" size="small" @click="removeFile(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
      <div class="muted" style="font-size: 11.5px; margin-top: 10px; line-height: 1.8">
        恢复方式：<code class="mono">mysql -u&lt;用户&gt; -p &lt;库名&gt; &lt; 备份文件</code>
        （gzip 文件先 <code class="mono">gunzip -c</code> 解压再导入）
      </div>
    </el-drawer>
  </div>
</template>

<script setup lang="ts">
import { onUnmounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh, Timer } from '@element-plus/icons-vue'
import { api, errMsg, getHostId, getToken } from '../api'
import { fmtBytes, fmtTime } from '../utils'

const props = defineProps<{
  schema: string
  databases: any[]
}>()

const loading = ref(false)
const dir = ref('')
const tasks = ref<any[]>([])

const weekNames = ['周日', '周一', '周二', '周三', '周四', '周五', '周六']

// 行数、表数这类整数用千分位，读起来比裸数字直观
function fmtNum(n?: number) {
  return Number(n || 0).toLocaleString('zh-CN')
}

// ---------- 列表 ----------

function hq() {
  return { host: getHostId() }
}

async function load() {
  loading.value = true
  try {
    const d = await api<any>('/api/dbbackup/tasks', { query: hq() })
    tasks.value = d.items || []
    dir.value = d.dir || ''
    schedulePoll()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

// 有任务在跑时自动轮询，跑完就停，避免一直打接口
let pollTimer: number | undefined
function schedulePoll() {
  const running = tasks.value.some((t) => t.last_status === 'running')
  if (running && !pollTimer) {
    pollTimer = window.setInterval(() => {
      if (!tasks.value.some((t) => t.last_status === 'running')) {
        stopPoll()
        return
      }
      load()
    }, 3000)
  } else if (!running) {
    stopPoll()
  }
}

function stopPoll() {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = undefined
  }
}

onUnmounted(stopPoll)

function statusText(s: string) {
  switch (s) {
    case 'ok':
      return '成功'
    case 'failed':
      return '失败'
    case 'running':
      return '备份中'
    default:
      return '未运行'
  }
}

// ---------- 新建 / 编辑 ----------

const dialogVisible = ref(false)
const saving = ref(false)
const formTables = ref<any[]>([])
const tableLoading = ref(false)
const form = reactive<any>({
  id: 0,
  name: '',
  schema: '',
  scope: 'full',
  tables: [] as string[],
  freq: 'daily',
  weekday: 1,
  month_day: 1,
  time: '02:00',
  keep: 7,
  compress: true,
  enabled: true
})

function resetForm() {
  Object.assign(form, {
    id: 0,
    name: '',
    schema: props.schema || '',
    scope: 'full',
    tables: [],
    freq: 'daily',
    weekday: 1,
    month_day: 1,
    time: '02:00',
    keep: 7,
    compress: true,
    enabled: true
  })
  formTables.value = []
}

async function openCreate() {
  resetForm()
  if (props.schema) await onFormSchemaChange(props.schema)
  dialogVisible.value = true
}

async function openEdit(row: any) {
  resetForm()
  Object.assign(form, {
    id: row.id,
    name: row.name,
    schema: row.schema,
    scope: row.scope,
    tables: [...(row.tables || [])],
    freq: row.freq,
    weekday: row.weekday,
    month_day: row.month_day,
    time: `${String(row.hour).padStart(2, '0')}:${String(row.minute).padStart(2, '0')}`,
    keep: row.keep,
    compress: !!row.compress,
    enabled: !!row.enabled
  })
  dialogVisible.value = true
  await onFormSchemaChange(row.schema)
}

// 切换库时把表列表换成该库的，旧选择直接清掉（表名不可能跨库有效）
async function onFormSchemaChange(name: string) {
  form.tables = []
  formTables.value = []
  if (!name) return
  tableLoading.value = true
  try {
    const d = await api<any>('/api/mysql/tables', { query: { schema: name } })
    formTables.value = (d.items || []).filter((t: any) => t.type !== 'view')
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    tableLoading.value = false
  }
}

async function save() {
  if (!form.schema) {
    ElMessage.warning('请选择要备份的数据库')
    return
  }
  const [hour, minute] = String(form.time || '02:00')
    .split(':')
    .map((v) => Number(v) || 0)
  const body = {
    name: form.name.trim(),
    schema: form.schema,
    tables: form.scope === 'tables' ? form.tables : [],
    freq: form.freq,
    hour,
    minute,
    weekday: form.weekday,
    month_day: form.month_day,
    keep: form.keep,
    compress: form.compress,
    enabled: form.enabled
  }
  saving.value = true
  try {
    if (form.id) {
      await api(`/api/dbbackup/tasks/${form.id}`, { method: 'PUT', body, query: hq() })
    } else {
      await api('/api/dbbackup/tasks', { method: 'POST', body, query: hq() })
    }
    ElMessage.success('已保存')
    dialogVisible.value = false
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
  }
}

// ---------- 操作 ----------

async function toggle(row: any, enabled: boolean) {
  try {
    await api(`/api/dbbackup/tasks/${row.id}/toggle`, {
      method: 'POST',
      body: { enabled },
      query: hq()
    })
    row.enabled = enabled
    ElMessage.success(enabled ? '已启用' : '已停用')
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
    await load()
  }
}

async function runNow(row: any) {
  try {
    await api(`/api/dbbackup/tasks/${row.id}/run`, { method: 'POST', query: hq() })
    ElMessage.success('已开始备份，跑完这里会自动刷新')
    row.last_status = 'running'
    schedulePoll()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function remove(row: any) {
  try {
    await ElMessageBox.confirm(
      `删除任务「${row.name}」会同时删掉它已生成的全部备份文件，且无法恢复。`,
      '确认删除备份任务',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消', confirmButtonClass: 'el-button--danger' }
    )
  } catch {
    return
  }
  try {
    await api(`/api/dbbackup/tasks/${row.id}`, { method: 'DELETE', query: hq() })
    ElMessage.success('已删除')
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

// ---------- 备份文件 ----------

const filesVisible = ref(false)
const filesLoading = ref(false)
const files = ref<any[]>([])
const filesTotal = ref(0)
const filesTask = ref<any>(null)

async function openFiles(row: any) {
  filesTask.value = row
  filesVisible.value = true
  await loadFiles()
}

async function loadFiles() {
  if (!filesTask.value) return
  filesLoading.value = true
  try {
    const d = await api<any>('/api/dbbackup/files', {
      query: { task_id: filesTask.value.id, host: getHostId() }
    })
    files.value = d.items || []
    filesTotal.value = d.total_size || 0
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    filesLoading.value = false
  }
}

function download(row: any) {
  if (!filesTask.value) return
  // 下载走浏览器原生跳转，token 只能放 query（与文件管理一致）
  const url =
    `/api/dbbackup/files/download?task_id=${filesTask.value.id}` +
    `&name=${encodeURIComponent(row.name)}&host=${getHostId()}&token=${encodeURIComponent(getToken())}`
  const a = document.createElement('a')
  a.href = url
  a.download = row.name
  document.body.appendChild(a)
  a.click()
  a.remove()
}

async function removeFile(row: any) {
  try {
    await ElMessageBox.confirm(`确认删除备份文件 ${row.name}？`, '删除备份文件', {
      type: 'warning',
      confirmButtonText: '删除',
      cancelButtonText: '取消'
    })
  } catch {
    return
  }
  try {
    await api('/api/dbbackup/files', {
      method: 'DELETE',
      query: { task_id: filesTask.value.id, name: row.name, host: getHostId() }
    })
    ElMessage.success('已删除')
    await loadFiles()
    await load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

defineExpose({ load })
</script>

<style scoped>
.bk-name {
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.bk-sub {
  font-size: 11.5px;
  gap: 5px;
  min-width: 0;
}
.bk-ellipsis {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
/* 错误信息只留一行，完整内容放 title，避免把行高撑到半屏 */
.bk-error {
  font-size: 11.5px;
  color: var(--danger);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
