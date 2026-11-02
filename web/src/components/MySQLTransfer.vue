<template>
  <div class="grid" style="grid-template-columns: repeat(2, minmax(0, 1fr)); align-items: start">
    <!-- 导出 -->
    <div class="card">
      <div class="card-title" style="margin-bottom: 4px">导出数据</div>
      <div class="muted" style="font-size: 12px; line-height: 1.8; margin-bottom: 14px">
        导出的文件直接由浏览器下载；SQL 格式带建表语句与分批 INSERT，可用
        <code class="mono">mysql 库名 &lt; 文件</code> 回放。
      </div>

      <el-form label-width="86px" label-position="left">
        <el-form-item label="数据来源">
          <el-radio-group v-model="exportForm.source" size="small">
            <el-radio-button label="table">整表</el-radio-button>
            <el-radio-button label="sql">查询结果</el-radio-button>
          </el-radio-group>
        </el-form-item>

        <template v-if="exportForm.source === 'table'">
          <el-form-item label="库 / 表">
            <div class="row" style="gap: 8px; width: 100%">
              <el-select v-model="exportForm.schema" filterable style="width: 150px" @change="loadExportTables">
                <el-option v-for="d in databases" :key="d.name" :label="d.name" :value="d.name" />
              </el-select>
              <el-select
                v-model="exportForm.table"
                filterable
                placeholder="选择表"
                style="flex: 1"
                :loading="exportTableLoading"
              >
                <el-option v-for="t in exportTables" :key="t.name" :label="t.name" :value="t.name" />
              </el-select>
            </div>
          </el-form-item>
        </template>

        <el-form-item v-else label="查询语句">
          <el-input
            v-model="exportForm.sql"
            type="textarea"
            :rows="4"
            class="mono"
            placeholder="SELECT * FROM users WHERE created_at > '2026-01-01'"
          />
        </el-form-item>

        <el-form-item label="文件格式">
          <el-radio-group v-model="exportForm.format" size="small">
            <el-radio-button label="csv">CSV</el-radio-button>
            <el-radio-button label="sql" :disabled="exportForm.source === 'sql'">SQL</el-radio-button>
          </el-radio-group>
          <span v-if="exportForm.source === 'sql'" class="muted" style="font-size: 11.5px; margin-left: 8px">
            查询结果只能导出 CSV
          </span>
        </el-form-item>

        <template v-if="exportForm.format === 'csv'">
          <el-form-item label="Excel 兼容">
            <el-switch v-model="exportForm.bom" />
            <span class="muted" style="font-size: 11.5px; margin-left: 10px">加 UTF-8 BOM，Excel 打开不乱码</span>
          </el-form-item>
          <el-form-item label="NULL 写法">
            <el-input
              v-model="exportForm.null_text"
              placeholder="留空"
              style="width: 120px"
              class="mono"
            />
            <span class="muted" style="font-size: 11.5px; margin-left: 10px">
              CSV 区分不了 NULL 与空串，需要保留时填 <code class="mono">\N</code>
            </span>
          </el-form-item>
        </template>

        <template v-else>
          <el-form-item label="包含内容">
            <el-checkbox v-model="exportForm.include_structure">建表语句</el-checkbox>
            <el-checkbox v-model="exportForm.include_data">数据</el-checkbox>
          </el-form-item>
        </template>

        <el-form-item label="行数上限">
          <el-input-number v-model="exportForm.limit" :min="0" :max="2000000" :step="10000" controls-position="right" />
          <span class="muted" style="font-size: 11.5px; margin-left: 10px">0 表示全表（最多 200 万行）</span>
        </el-form-item>
      </el-form>

      <div class="row" style="margin-top: 6px">
        <span v-if="lastExport" class="muted" style="font-size: 12px">{{ lastExport }}</span>
        <div class="spacer"></div>
        <el-button type="primary" :loading="exporting" :icon="Download" @click="doExport">导出</el-button>
      </div>
    </div>

    <!-- 导入 -->
    <div class="card">
      <div class="card-title" style="margin-bottom: 4px">导入数据</div>
      <div class="muted" style="font-size: 12px; line-height: 1.8; margin-bottom: 14px">
        CSV 按列名匹配目标表，列顺序可以不同；SQL 文件逐条执行，失败语句会跳过并记录。
        文件上限 64 MB。
      </div>

      <el-form label-width="86px" label-position="left">
        <el-form-item label="目标库">
          <el-select v-model="importForm.schema" filterable style="width: 200px" @change="loadImportTables">
            <el-option v-for="d in databases" :key="d.name" :label="d.name" :value="d.name" />
          </el-select>
        </el-form-item>
        <el-form-item label="目标表">
          <el-select
            v-model="importForm.table"
            filterable
            clearable
            :placeholder="importForm.format === 'csv' ? 'CSV 必选' : '可留空（文件自带 USE / 建表）'"
            style="width: 260px"
            :loading="importTableLoading"
          >
            <el-option v-for="t in importTables" :key="t.name" :label="t.name" :value="t.name" />
          </el-select>
        </el-form-item>

        <el-form-item label="文件">
          <el-upload
            drag
            :auto-upload="false"
            :show-file-list="false"
            accept=".csv,.sql,.txt"
            style="width: 100%"
            @change="onFileChange"
          >
            <div style="padding: 10px 0">
              <el-icon :size="22" style="color: var(--ink-3)"><UploadFilled /></el-icon>
              <div style="font-size: 12.5px; margin-top: 6px">
                {{ pickedName || '点击或拖拽文件到这里' }}
              </div>
              <div class="muted" style="font-size: 11.5px; margin-top: 2px">
                {{ pickedSize ? fmtSize(pickedSize) : '支持 .csv / .sql' }}
              </div>
            </div>
          </el-upload>
        </el-form-item>

        <el-form-item label="格式">
          <el-radio-group v-model="importForm.format" size="small">
            <el-radio-button label="csv">CSV</el-radio-button>
            <el-radio-button label="sql">SQL</el-radio-button>
          </el-radio-group>
        </el-form-item>

        <template v-if="importForm.format === 'csv'">
          <el-form-item label="首行">
            <el-checkbox v-model="importForm.has_header">首行是列名（按列名匹配，不依赖顺序）</el-checkbox>
          </el-form-item>
          <el-form-item label="导入前">
            <el-checkbox v-model="importForm.truncate">清空目标表（TRUNCATE）</el-checkbox>
          </el-form-item>
          <el-form-item label="NULL 标记">
            <el-input v-model="importForm.null_text" placeholder="NULL" style="width: 140px" class="mono" />
            <span class="muted" style="font-size: 11.5px; margin-left: 8px">默认已识别 NULL 与 \N</span>
          </el-form-item>
        </template>

        <el-form-item label="失败上限">
          <el-input-number v-model="importForm.max_errors" :min="1" :max="10000" controls-position="right" />
          <span class="muted" style="font-size: 11.5px; margin-left: 10px">超过后中止，已导入的不回滚</span>
        </el-form-item>
      </el-form>

      <div class="row" style="margin-top: 6px">
        <span v-if="readOnly" class="muted" style="font-size: 12px; color: #b25a00">
          当前连接是只读模式，导入会被拒绝
        </span>
        <div class="spacer"></div>
        <el-button
          type="primary"
          :loading="importing"
          :disabled="readOnly || !pickedFile"
          :icon="Upload"
          @click="doImport"
        >开始导入</el-button>
      </div>

      <div v-if="importResult" style="margin-top: 14px">
        <el-divider style="margin: 8px 0 12px" />
        <div class="row" style="gap: 18px; font-size: 13px">
          <span>成功 <b style="color: #1a7f37">{{ fmtNum(importResult.imported) }}</b></span>
          <span>失败 <b :style="{ color: importResult.failed ? '#c2382f' : 'inherit' }">
            {{ fmtNum(importResult.failed) }}</b></span>
          <span class="muted">共 {{ fmtNum(importResult.total) }} 条 · {{ importResult.cost_ms }} ms</span>
          <el-tag v-if="importResult.truncated" size="small" type="warning" effect="plain">已中止</el-tag>
        </div>
        <div v-if="importResult.messages?.length" class="msg-box mono">
          <div v-for="(m, i) in importResult.messages" :key="i">{{ m }}</div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Download, Upload, UploadFilled } from '@element-plus/icons-vue'
import { api, errMsg, getHostId, getToken } from '../api'

const props = withDefaults(
  defineProps<{
    schema: string
    databases: any[]
    tables: any[]
    readOnly: boolean
    initialTable?: string
  }>(),
  { initialTable: '' }
)

const exportForm = reactive({
  source: 'table',
  schema: '',
  table: '',
  sql: '',
  format: 'csv',
  bom: true,
  null_text: '',
  include_structure: true,
  include_data: true,
  limit: 0
})
const exporting = ref(false)
const lastExport = ref('')
const exportTables = ref<any[]>([])
const exportTableLoading = ref(false)

const importForm = reactive({
  schema: '',
  table: '',
  format: 'csv',
  has_header: true,
  truncate: false,
  null_text: '',
  max_errors: 50
})
const importing = ref(false)
const importTables = ref<any[]>([])
const importTableLoading = ref(false)
const pickedFile = ref<File | null>(null)
const pickedName = ref('')
const pickedSize = ref(0)
const importResult = ref<any>(null)

function fmtNum(n: any) {
  const v = Number(n || 0)
  return Number.isFinite(v) ? v.toLocaleString('zh-CN') : '-'
}

function fmtSize(n: any) {
  const v = Number(n || 0)
  if (!Number.isFinite(v) || v <= 0) return '0 B'
  if (v < 1024) return v + ' B'
  const units = ['KB', 'MB', 'GB']
  let val = v / 1024
  let i = 0
  while (val >= 1024 && i < units.length - 1) {
    val /= 1024
    i++
  }
  return val.toFixed(val >= 100 ? 0 : 1) + ' ' + units[i]
}

async function loadTableList(schema: string, target: typeof exportTables) {
  if (!schema) {
    target.value = []
    return
  }
  const res = await api<any>('/api/mysql/tables', { query: { schema } })
  target.value = (res?.items || []).filter((t: any) => t.type === 'table')
}

async function loadExportTables() {
  exportTableLoading.value = true
  try {
    await loadTableList(exportForm.schema, exportTables)
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    exportTableLoading.value = false
  }
}

async function loadImportTables() {
  importTableLoading.value = true
  try {
    await loadTableList(importForm.schema, importTables)
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    importTableLoading.value = false
  }
}

// 父页面切库时同步过来（两个卡片都跟着切，避免导入导出跑到别的库上）
watch(
  () => props.schema,
  (v) => {
    if (!v) return
    exportForm.schema = v
    importForm.schema = v
    exportForm.table = ''
    importForm.table = ''
    loadExportTables()
    loadImportTables()
  },
  { immediate: true }
)

// 查询结果只能导 CSV，切到查询来源时把格式拉回来
watch(
  () => exportForm.source,
  (v) => {
    if (v === 'sql' && exportForm.format === 'sql') exportForm.format = 'csv'
  }
)

// 表列表里点「导出」跳过来时，直接定位到该表
watch(
  () => props.initialTable,
  (v) => {
    if (!v) return
    exportForm.source = 'table'
    exportForm.schema = props.schema
    exportForm.table = v
    loadExportTables()
  }
)

function onFileChange(file: any) {
  pickedFile.value = file.raw || null
  pickedName.value = file.name || ''
  pickedSize.value = file.size || file.raw?.size || 0
  const lower = pickedName.value.toLowerCase()
  if (lower.endsWith('.sql')) importForm.format = 'sql'
  else if (lower.endsWith('.csv')) importForm.format = 'csv'
  importResult.value = null
}

// ---------- 导出 ----------

function filenameFrom(disposition: string | null): string {
  if (!disposition) return ''
  const star = /filename\*=UTF-8''([^;]+)/i.exec(disposition)
  if (star) {
    try {
      return decodeURIComponent(star[1])
    } catch {
      /* 忽略编码异常，退回下一种解析 */
    }
  }
  const plain = /filename="?([^";]+)"?/i.exec(disposition)
  return plain ? plain[1] : ''
}

async function doExport() {
  if (exportForm.source === 'table') {
    if (!exportForm.schema || !exportForm.table) {
      ElMessage.warning('请选择要导出的表')
      return
    }
  } else if (!exportForm.sql.trim()) {
    ElMessage.warning('请输入查询语句')
    return
  }

  exporting.value = true
  lastExport.value = ''
  try {
    const res = await fetch(`/api/mysql/export?host=${getHostId()}`, {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${getToken()}`,
        'Content-Type': 'application/json'
      },
      body: JSON.stringify({
        schema: exportForm.schema,
        table: exportForm.source === 'table' ? exportForm.table : '',
        sql: exportForm.source === 'sql' ? exportForm.sql : '',
        format: exportForm.format,
        bom: exportForm.bom,
        null_text: exportForm.null_text,
        include_structure: exportForm.include_structure,
        include_data: exportForm.include_data,
        limit: exportForm.limit
      })
    })

    const ct = res.headers.get('Content-Type') || ''
    if (!res.ok || ct.includes('application/json')) {
      const text = await res.text()
      let msg = `导出失败 (${res.status})`
      try {
        msg = JSON.parse(text)?.msg || msg
      } catch {
        /* 非 JSON 时保留默认提示 */
      }
      throw new Error(msg)
    }

    const blob = await res.blob()
    if (!blob.size) throw new Error('导出内容为空')
    const name = filenameFrom(res.headers.get('Content-Disposition')) || 'export.csv'
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = name
    document.body.appendChild(a)
    a.click()
    a.remove()
    // 交给浏览器读取后再释放
    setTimeout(() => URL.revokeObjectURL(url), 4000)
    lastExport.value = `已导出 ${name}（${fmtSize(blob.size)}）`
    ElMessage.success(lastExport.value)
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    exporting.value = false
  }
}

// ---------- 导入 ----------

async function doImport() {
  if (!pickedFile.value) return
  if (importForm.format === 'csv' && (!importForm.schema || !importForm.table)) {
    ElMessage.warning('CSV 导入必须指定目标库与目标表')
    return
  }
  try {
    await ElMessageBox.confirm(
      importForm.truncate
        ? '导入前会先清空目标表，现有数据将全部丢失。确定继续吗？'
        : '导入会直接写入数据库，确定继续吗？',
      '确认导入',
      { type: 'warning', confirmButtonText: '开始导入', confirmButtonClass: 'el-button--danger' }
    )
  } catch {
    return
  }

  importing.value = true
  importResult.value = null
  try {
    const qs = new URLSearchParams({
      format: importForm.format,
      schema: importForm.schema,
      table: importForm.table || '',
      has_header: importForm.has_header ? '1' : '0',
      truncate: importForm.truncate ? '1' : '0',
      null_text: importForm.null_text || '',
      max_errors: String(importForm.max_errors),
      host: String(getHostId())
    })
    const fd = new FormData()
    fd.append('file', pickedFile.value)
    const res = await fetch(`/api/mysql/import?${qs.toString()}`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${getToken()}` },
      body: fd
    })
    const text = await res.text()
    let payload: any = null
    try {
      payload = text ? JSON.parse(text) : null
    } catch {
      /* 保留原始文本用于报错 */
    }
    if (!res.ok || (payload && payload.code !== 0)) {
      throw new Error(payload?.msg || text || `导入失败 (${res.status})`)
    }
    importResult.value = payload.data
    if (payload.data?.failed) {
      ElMessage.warning(`导入完成，${payload.data.failed} 条失败，详见下方记录`)
    } else {
      ElMessage.success(`导入完成，共 ${payload.data?.imported || 0} 条`)
    }
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    importing.value = false
  }
}

onMounted(() => {
  if (props.initialTable) {
    exportForm.source = 'table'
    exportForm.table = props.initialTable
  }
})
</script>

<style scoped>
.msg-box {
  margin-top: 10px;
  padding: 10px 12px;
  background: #fff8f7;
  border: 1px solid #f3d7d3;
  border-radius: 8px;
  font-size: 11.5px;
  line-height: 1.9;
  color: #9c2b23;
  max-height: 220px;
  overflow: auto;
  word-break: break-all;
}
</style>
