<template>
  <div class="grid" style="grid-template-columns: minmax(0, 1fr); gap: 12px">
    <!-- 顶部：模式与目标 -->
    <div class="card" style="padding: 12px 16px">
      <div class="row" style="flex-wrap: wrap; gap: 10px">
        <el-radio-group v-model="mode" size="small" @change="onModeChange">
          <el-radio-button label="create">新建表</el-radio-button>
          <el-radio-button label="alter" :disabled="!tables.length">修改现有表</el-radio-button>
        </el-radio-group>

        <span class="muted" style="font-size: 12px">库</span>
        <el-select v-model="schema" filterable style="width: 160px" @change="onSchemaChange">
          <el-option v-for="d in databases" :key="d.name" :label="d.name" :value="d.name" />
        </el-select>

        <template v-if="mode === 'create'">
          <span class="muted" style="font-size: 12px">表名</span>
          <el-input v-model="tableName" placeholder="例如 orders" class="mono" style="width: 200px" />
        </template>
        <template v-else>
          <span class="muted" style="font-size: 12px">表</span>
          <el-select
            v-model="tableName"
            filterable
            placeholder="选择要修改的表"
            style="width: 220px"
            :loading="structureLoading"
            @change="loadStructure"
          >
            <el-option v-for="t in alterTables" :key="t.name" :label="t.name" :value="t.name" />
          </el-select>
          <el-button size="small" :icon="Refresh" :disabled="!tableName" @click="loadStructure">重新加载</el-button>
        </template>

        <div class="spacer"></div>
        <span v-if="readOnly" class="muted" style="font-size: 12px; color: #b25a00">只读模式：只能预览，不能执行</span>
      </div>
    </div>

    <!-- 字段 -->
    <div class="card" style="padding: 0">
      <div class="toolbar" style="padding: 12px 14px 8px">
        <span class="card-title">字段</span>
        <span class="muted" style="font-size: 12px; margin-left: 8px">
          共 {{ columns.length }} 个<span v-if="removedCount">，(移除 {{ removedCount }} 个未启用删除)</span>
        </span>
        <div class="spacer"></div>
        <el-button size="small" :icon="Plus" @click="addColumn">添加字段</el-button>
      </div>

      <el-table :data="columns" size="small" v-loading="structureLoading" border style="width: 100%">
        <el-table-column label="#" width="46" align="center">
          <template #default="{ $index }"><span class="muted mono">{{ $index + 1 }}</span></template>
        </el-table-column>
        <el-table-column label="字段名" min-width="140">
          <template #default="{ row }">
            <el-input v-model="row.name" size="small" class="mono" placeholder="列名" />
          </template>
        </el-table-column>
        <el-table-column label="类型" width="150">
          <template #default="{ row }">
            <el-select
              v-model="row.type"
              size="small"
              filterable
              allow-create
              default-first-option
              @change="onTypeChange(row)"
            >
              <el-option-group v-for="g in typeGroups" :key="g.label" :label="g.label">
                <el-option v-for="t in g.options" :key="t" :label="t" :value="t" />
              </el-option-group>
            </el-select>
          </template>
        </el-table-column>
        <el-table-column label="长度 / 取值" width="130">
          <template #default="{ row }">
            <el-input
              v-model="row.length"
              size="small"
              class="mono"
              :placeholder="argPlaceholder(row.type)"
              :disabled="noArgType(row.type)"
            />
          </template>
        </el-table-column>
        <el-table-column label="无符号" width="70" align="center">
          <template #default="{ row }">
            <el-checkbox v-model="row.unsigned" :disabled="!isNumeric(row.type)" />
          </template>
        </el-table-column>
        <el-table-column label="可空" width="60" align="center">
          <template #default="{ row }"><el-checkbox v-model="row.nullable" /></template>
        </el-table-column>
        <el-table-column label="默认值" width="130">
          <template #default="{ row }">
            <el-input
              v-model="row.default_text"
              size="small"
              class="mono"
              placeholder="无"
            />
          </template>
        </el-table-column>
        <el-table-column label="自增" width="60" align="center">
          <template #default="{ row }">
            <el-checkbox v-model="row.auto_increment" :disabled="!isInteger(row.type)" />
          </template>
        </el-table-column>
        <el-table-column label="主键" width="60" align="center">
          <template #default="{ row }"><el-checkbox v-model="row.primary" /></template>
        </el-table-column>
        <el-table-column label="注释" min-width="130">
          <template #default="{ row }">
            <el-input v-model="row.comment" size="small" placeholder="可选" />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="60" align="center">
          <template #default="{ $index }">
            <el-button link type="danger" size="small" :icon="Delete" @click="columns.splice($index, 1)" />
          </template>
        </el-table-column>
      </el-table>
      <div class="muted" style="font-size: 11.5px; padding: 8px 14px 12px; line-height: 1.8">
        默认值留空表示不设置；要「默认空字符串」请填 <code class="mono">''</code>，要 NULL 请填
        <code class="mono">NULL</code>，时间戳可填 <code class="mono">CURRENT_TIMESTAMP</code>。
      </div>
    </div>

    <!-- 索引 -->
    <div class="card" style="padding: 0">
      <div class="toolbar" style="padding: 12px 14px 8px">
        <span class="card-title">索引</span>
        <span class="muted" style="font-size: 12px; margin-left: 8px">主键也可以单独建一条名为 PRIMARY 的索引</span>
        <div class="spacer"></div>
        <el-button size="small" :icon="Plus" @click="addIndex">添加索引</el-button>
      </div>
      <el-table :data="indexes" size="small" border empty-text="没有额外索引（主键由字段上的勾选决定）">
        <el-table-column label="索引名" width="180">
          <template #default="{ row }">
            <el-input v-model="row.name" size="small" class="mono" placeholder="留空自动命名" />
          </template>
        </el-table-column>
        <el-table-column label="类型" width="130">
          <template #default="{ row }">
            <el-select v-model="row.unique" size="small">
              <el-option label="普通索引" :value="false" />
              <el-option label="唯一索引" :value="true" />
            </el-select>
          </template>
        </el-table-column>
        <el-table-column label="字段" min-width="220">
          <template #default="{ row }">
            <el-select v-model="row.columns" size="small" multiple filterable style="width: 100%">
              <el-option v-for="c in columnNames" :key="c" :label="c" :value="c" />
            </el-select>
          </template>
        </el-table-column>
        <el-table-column label="算法" width="120">
          <template #default="{ row }">
            <el-select v-model="row.kind" size="small">
              <el-option label="BTREE" value="" />
              <el-option label="FULLTEXT" value="FULLTEXT" />
              <el-option label="SPATIAL" value="SPATIAL" />
            </el-select>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="60" align="center">
          <template #default="{ $index }">
            <el-button link type="danger" size="small" :icon="Delete" @click="indexes.splice($index, 1)" />
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 表选项 + 操作 -->
    <div class="card">
      <div class="row" style="flex-wrap: wrap; gap: 10px">
        <span class="muted" style="font-size: 12px">引擎</span>
        <el-select v-model="engine" style="width: 130px" size="small">
          <el-option v-for="e in engineOptions" :key="e" :label="e" :value="e" />
        </el-select>
        <span class="muted" style="font-size: 12px">字符集</span>
        <el-select v-model="charset" style="width: 130px" size="small" clearable>
          <el-option v-for="c in charsetOptions" :key="c" :label="c" :value="c" />
        </el-select>
        <span class="muted" style="font-size: 12px">排序规则</span>
        <el-input v-model="collation" placeholder="可选" size="small" class="mono" style="width: 180px" />
        <span class="muted" style="font-size: 12px">表注释</span>
        <el-input v-model="tableComment" placeholder="可选" size="small" style="width: 200px" />

        <template v-if="mode === 'alter'">
          <el-checkbox v-model="dropExtra">
            删除编辑中移除的字段与索引
          </el-checkbox>
        </template>

        <div class="spacer"></div>
        <el-button :loading="previewing" @click="preview">预览 DDL</el-button>
        <el-button
          type="primary"
          :loading="executing"
          :disabled="readOnly || !previewDDL"
          @click="execute"
        >执行</el-button>
      </div>

      <el-alert
        v-if="mode === 'create'"
        type="info"
        :closable="false"
        show-icon
        style="margin-top: 12px"
        title="建表只在点击「执行」后生效；建议先点「预览 DDL」确认语句"
      />

      <div v-if="changes.length" style="margin-top: 12px">
        <div class="muted" style="font-size: 12px; margin-bottom: 6px">将发生的变更（{{ changes.length }} 项）</div>
        <div class="row" style="flex-wrap: wrap; gap: 6px">
          <el-tag v-for="(c, i) in changes" :key="i" size="small" effect="plain">{{ c }}</el-tag>
        </div>
      </div>

      <div v-if="previewDDL" style="margin-top: 12px">
        <div class="row" style="margin-bottom: 6px">
          <span class="muted" style="font-size: 12px">生成的 SQL</span>
          <div class="spacer"></div>
          <el-button size="small" @click="copySQL">复制</el-button>
        </div>
        <pre class="preview mono">{{ previewDDL }}</pre>
      </div>
      <div v-else-if="previewError" class="cmd-err mono" style="margin-top: 12px">{{ previewError }}</div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete, Plus, Refresh } from '@element-plus/icons-vue'
import { api, errMsg } from '../api'
import { copyText } from '../utils'

const props = defineProps<{
  schema: string
  databases: any[]
  tables: any[]
  readOnly: boolean
}>()

const emit = defineEmits<{ (e: 'refresh'): void }>()

interface ColRow {
  name: string
  original_name: string
  type: string
  length: string
  unsigned: boolean
  nullable: boolean
  auto_increment: boolean
  default_text: string
  comment: string
  primary: boolean
}

const mode = ref<'create' | 'alter'>('create')
const schema = ref('')
const tableName = ref('')
const columns = ref<ColRow[]>([])
const indexes = ref<any[]>([])
const engine = ref('InnoDB')
const charset = ref('utf8mb4')
const collation = ref('')
const tableComment = ref('')
const dropExtra = ref(false)
const structureLoading = ref(false)
const previewing = ref(false)
const executing = ref(false)
const previewDDL = ref('')
const previewError = ref('')
const changes = ref<string[]>([])
const originalNames = ref<string[]>([])

const typeGroups = [
  { label: '整数', options: ['tinyint', 'smallint', 'mediumint', 'int', 'bigint'] },
  { label: '小数', options: ['decimal', 'float', 'double'] },
  { label: '字符串', options: ['char', 'varchar', 'tinytext', 'text', 'mediumtext', 'longtext'] },
  { label: '二进制', options: ['binary', 'varbinary', 'tinyblob', 'blob', 'mediumblob', 'longblob'] },
  { label: '时间', options: ['date', 'datetime', 'timestamp', 'time', 'year'] },
  { label: '其他', options: ['json', 'enum', 'set', 'bool'] }
]
const engineOptions = ['InnoDB', 'MyISAM', 'MEMORY', 'ARCHIVE', 'CSV']
const charsetOptions = ['utf8mb4', 'utf8mb3', 'utf8', 'latin1', 'gbk', 'big5']

const columnNames = computed(() => columns.value.map((c) => c.name).filter(Boolean))
// 视图不能用 ALTER TABLE 改结构，只列出真正的表
const alterTables = computed(() => props.tables.filter((t: any) => t.type === 'table'))
const removedCount = computed(() => {
  if (mode.value !== 'alter') return 0
  const kept = new Set(columns.value.map((c) => (c.original_name || c.name).toLowerCase()))
  return originalNames.value.filter((n) => !kept.has(n.toLowerCase())).length
})

function isNumeric(t: string) {
  return ['tinyint', 'smallint', 'mediumint', 'int', 'integer', 'bigint', 'decimal', 'numeric',
    'float', 'double', 'real', 'bit', 'year'].includes((t || '').toLowerCase())
}

function isInteger(t: string) {
  return ['tinyint', 'smallint', 'mediumint', 'int', 'integer', 'bigint'].includes((t || '').toLowerCase())
}

function noArgType(t: string) {
  return ['date', 'datetime', 'timestamp', 'time', 'json', 'bool', 'boolean', 'tinytext', 'text',
    'mediumtext', 'longtext', 'tinyblob', 'blob', 'mediumblob', 'longblob'].includes((t || '').toLowerCase())
}

function argPlaceholder(t: string) {
  const k = (t || '').toLowerCase()
  if (k === 'enum' || k === 'set') return "'a','b'"
  if (noArgType(k)) return '-'
  if (k === 'decimal' || k === 'numeric') return '10,2'
  if (['char', 'varchar', 'binary', 'varbinary'].includes(k)) return '255'
  return '可选'
}

function onTypeChange(row: ColRow) {
  const k = (row.type || '').toLowerCase()
  if (noArgType(k)) {
    row.length = ''
    return
  }
  if (k === 'enum' || k === 'set') {
    if (!row.length || !row.length.includes("'")) row.length = "'a','b'"
    return
  }
  if (!row.length) {
    if (k === 'varchar' || k === 'char' || k === 'binary' || k === 'varbinary') row.length = '255'
    else if (k === 'decimal' || k === 'numeric') row.length = '10,2'
  }
}

function blankColumn(): ColRow {
  return {
    name: '',
    original_name: '',
    type: 'varchar',
    length: '255',
    unsigned: false,
    nullable: true,
    auto_increment: false,
    default_text: '',
    comment: '',
    primary: false
  }
}

function addColumn() {
  columns.value.push(blankColumn())
}

function addIndex() {
  indexes.value.push({ name: '', columns: [], unique: false, kind: '' })
}

// ---------- 模式与结构加载 ----------

function onModeChange() {
  previewDDL.value = ''
  previewError.value = ''
  changes.value = []
  if (mode.value === 'create') {
    tableName.value = ''
    columns.value = [blankColumn()]
    indexes.value = []
    originalNames.value = []
  } else {
    tableName.value = ''
    columns.value = []
    indexes.value = []
  }
}

function onSchemaChange() {
  if (mode.value === 'alter') {
    tableName.value = ''
    columns.value = []
    indexes.value = []
  }
}

// varchar(255) → { type: 'varchar', length: '255' }
function parseType(raw: string) {
  let s = (raw || '').toLowerCase().trim()
  let unsigned = false
  if (s.endsWith(' unsigned')) {
    unsigned = true
    s = s.slice(0, -9).trim()
  }
  const m = /^([a-z]+)(?:\((.*)\))?$/.exec(s)
  if (!m) return { type: s, length: '', unsigned }
  return { type: m[1], length: m[2] || '', unsigned }
}

async function loadStructure() {
  if (!tableName.value) return
  structureLoading.value = true
  previewDDL.value = ''
  changes.value = []
  try {
    const detail = await api<any>('/api/mysql/table', {
      query: { schema: schema.value, table: tableName.value }
    })
    const cols: ColRow[] = (detail?.columns || []).map((c: any) => {
      const p = parseType(c.type)
      // information_schema 里「无默认值」与「默认空串」都是空串，靠 default_null 区分；
      // 显式空串默认值映射成 '' 字面量，回写时才能原样保留
      let defaultText = ''
      if (!c.default_null) defaultText = c.default === '' ? "''" : c.default
      return {
        name: c.name,
        original_name: c.name,
        type: p.type,
        length: p.length,
        unsigned: p.unsigned,
        nullable: !!c.nullable,
        auto_increment: /auto_increment/i.test(c.extra || ''),
        default_text: defaultText,
        comment: c.comment || '',
        primary: c.key === 'PRI'
      }
    })
    columns.value = cols
    originalNames.value = cols.map((c) => c.name)
    indexes.value = (detail?.indexes || [])
      .filter((i: any) => i.name !== 'PRIMARY')
      .map((i: any) => ({
        name: i.name,
        columns: i.columns || [],
        unique: !!i.unique,
        kind: i.type === 'FULLTEXT' || i.type === 'SPATIAL' ? i.type : ''
      }))
    // 表选项原样带回，避免「只是改了字段」却顺手把引擎/字符集/注释也改掉
    engine.value = detail?.engine || 'InnoDB'
    charset.value = detail?.charset || ''
    collation.value = ''
    tableComment.value = detail?.comment || ''
  } catch (e) {
    ElMessage.error(errMsg(e))
    columns.value = []
  } finally {
    structureLoading.value = false
  }
}

// ---------- 提交 ----------

function columnPayload() {
  return columns.value
    .filter((c) => (c.name || '').trim() !== '')
    .map((c) => ({
      name: c.name.trim(),
      original_name: mode.value === 'alter' ? c.original_name : '',
      type: c.type,
      length: c.length,
      unsigned: c.unsigned,
      nullable: c.nullable,
      auto_increment: c.auto_increment,
      default: (c.default_text || '').trim() === '' ? null : c.default_text,
      on_update: '',
      comment: c.comment,
      charset: '',
      collation: '',
      primary: c.primary
    }))
}

function buildBody() {
  const idx = indexes.value
    .filter((i) => (i.columns || []).length > 0)
    .map((i) => ({ name: i.name, columns: i.columns, unique: !!i.unique, kind: i.kind }))

  // 改表模式下主键通过「字段上的主键勾选」表达，这里翻译成一条 PRIMARY 索引，
  // 后端才能判断主键是没变、要重建、还是要删掉。
  if (mode.value === 'alter') {
    const pk = columnPayload().filter((c) => c.primary).map((c) => c.name)
    if (pk.length) idx.unshift({ name: 'PRIMARY', columns: pk, unique: true, kind: '' })
  }

  return {
    schema: schema.value,
    table: tableName.value.trim(),
    engine: engine.value,
    charset: charset.value,
    collation: collation.value,
    comment: tableComment.value,
    drop_extra: mode.value === 'alter' ? dropExtra.value : false,
    columns: columnPayload(),
    indexes: idx
  }
}

function validate() {
  if (!schema.value) return '请选择数据库'
  if (!tableName.value.trim()) return '请填写表名'
  if (!columnPayload().length) return '至少要有一个字段'
  return ''
}

async function preview() {
  const bad = validate()
  if (bad) return ElMessage.warning(bad)
  previewing.value = true
  previewError.value = ''
  changes.value = []
  try {
    const path = mode.value === 'create' ? '/api/mysql/table/create' : '/api/mysql/table/alter'
    const res = await api<any>(`${path}?dry_run=1`, { method: 'POST', body: buildBody() })
    previewDDL.value = res?.ddl || ''
    changes.value = res?.changes || []
    if (!previewDDL.value) {
      previewError.value = mode.value === 'alter' ? '没有检测到结构变化' : '没有生成语句'
    }
  } catch (e) {
    previewDDL.value = ''
    previewError.value = errMsg(e)
  } finally {
    previewing.value = false
  }
}

async function execute() {
  if (!previewDDL.value) return
  if (props.readOnly) return ElMessage.warning('当前连接是只读模式')
  try {
    await ElMessageBox.confirm(
      mode.value === 'create'
        ? `将在 ${schema.value} 创建表 ${tableName.value}，确定执行吗？`
        : `将修改表 ${schema.value}.${tableName.value} 的结构（${changes.value.length} 项变更），` +
          '大表上的变更可能长时间锁表，确定执行吗？',
      mode.value === 'create' ? '确认建表' : '确认改表',
      { type: 'warning', confirmButtonText: '执行', confirmButtonClass: 'el-button--danger' }
    )
  } catch {
    return
  }

  executing.value = true
  try {
    const path = mode.value === 'create' ? '/api/mysql/table/create' : '/api/mysql/table/alter'
    const res = await api<any>(path, { method: 'POST', body: buildBody() })
    ElMessage.success(mode.value === 'create' ? '表已创建' : '表结构已更新')
    previewDDL.value = res?.ddl || previewDDL.value
    if (mode.value === 'alter') await loadStructure()
    emit('refresh')
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    executing.value = false
  }
}

async function copySQL() {
  const ok = await copyText(previewDDL.value)
  if (ok) ElMessage.success('已复制')
  else ElMessage.error('复制失败，请手动选择文本')
}

// 跟随顶部切库：表属于库，库变了原来的选择就失效了
watch(
  () => props.schema,
  (v) => {
    if (!v || v === schema.value) return
    schema.value = v
    if (mode.value === 'alter') {
      tableName.value = ''
      columns.value = []
      indexes.value = []
      previewDDL.value = ''
      changes.value = []
    }
  },
  { immediate: true }
)

onMounted(() => {
  schema.value = schema.value || props.schema
  columns.value = [blankColumn()]
})
</script>

<style scoped>
.preview {
  background: #f8f9fc;
  border: 1px solid var(--line);
  border-radius: 8px;
  padding: 12px 14px;
  font-size: 12.5px;
  line-height: 1.7;
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 320px;
  overflow: auto;
  margin: 0;
}
.cmd-err {
  padding: 10px 12px;
  font-size: 12.5px;
  color: #c2382f;
  background: #fff8f7;
  border: 1px solid #f3d7d3;
  border-radius: 8px;
  white-space: pre-wrap;
  word-break: break-all;
}
</style>
