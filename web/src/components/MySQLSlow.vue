<template>
  <div>
    <!-- 服务端开关 -->
    <div class="card" style="padding: 12px 16px">
      <div class="row" style="flex-wrap: wrap; gap: 8px">
        <span class="stat-pill" :class="psOn ? 'ok' : 'bad'">
          <span class="dot" :class="psOn ? 'green' : 'red'"></span>
          performance_schema {{ psOn ? 'ON' : settings.performance_schema || '未知' }}
        </span>
        <el-tag size="small" effect="plain" :type="slowLogOn ? 'success' : 'info'">
          slow_query_log {{ slowLogOn ? 'ON' : 'OFF' }}
        </el-tag>
        <el-tag size="small" effect="plain">long_query_time {{ settings.long_query_time || '-' }} s</el-tag>
        <el-tag size="small" effect="plain">log_output {{ settings.log_output || '-' }}</el-tag>
        <el-tag v-if="settings.log_queries_not_using_indexes === 'ON'" size="small" effect="plain" type="warning">
          记录未走索引的语句
        </el-tag>
        <span v-if="settings.slow_query_log_file" class="muted mono" style="font-size: 11.5px">
          {{ settings.slow_query_log_file }}
        </span>
      </div>
      <div class="muted" style="font-size: 11.5px; margin-top: 8px; line-height: 1.8">
        慢查询来自 <code class="mono">performance_schema.events_statements_summary_by_digest</code>：
        同一类语句按模板（参数用 <code class="mono">?</code> 代替）聚合，与 slow_query_log 的阈值无关，
        所以这里能看到「平均耗时较高」的语句全貌。
      </div>
    </div>

    <!-- 筛选 -->
    <div class="card" style="padding: 12px 14px; margin-top: 12px">
      <div class="row" style="flex-wrap: wrap; gap: 10px">
        <span class="muted" style="font-size: 12px">库</span>
        <el-select v-model="filters.schema" clearable placeholder="全部" style="width: 170px" size="small">
          <el-option v-for="d in databases" :key="d.name" :label="d.name" :value="d.name" />
        </el-select>
        <span class="muted" style="font-size: 12px">平均耗时 ≥</span>
        <el-input-number v-model="filters.min_ms" :min="0" :max="600000" :step="10" size="small" controls-position="right" style="width: 130px" />
        <span class="muted" style="font-size: 12px">ms</span>
        <span class="muted" style="font-size: 12px; margin-left: 6px">排序</span>
        <el-select v-model="filters.order" style="width: 130px" size="small">
          <el-option label="平均耗时" value="avg" />
          <el-option label="最大耗时" value="max" />
          <el-option label="总耗时" value="total" />
          <el-option label="执行次数" value="count" />
          <el-option label="扫描行数" value="examined" />
        </el-select>
        <el-select v-model="filters.limit" style="width: 110px" size="small">
          <el-option :label="'前 50 条'" :value="50" />
          <el-option :label="'前 100 条'" :value="100" />
          <el-option :label="'前 200 条'" :value="200" />
        </el-select>
        <div class="spacer"></div>
        <span v-if="loading" class="muted" style="font-size: 12px">正在读取…</span>
        <el-button size="small" :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
      </div>
    </div>

    <!-- 主体 -->
    <div class="card" style="padding: 0; margin-top: 12px">
      <el-alert
        v-if="!loading && report.available === false"
        type="warning"
        :closable="false"
        show-icon
        style="margin: 12px 14px"
        :title="report.error || '无法读取 performance_schema'"
        :description="report.hint || ''"
      />

      <el-table
        v-if="report.available"
        :data="report.items"
        v-loading="loading"
        size="small"
        max-height="calc(100vh - 470px)"
        empty-text="没有符合条件的慢语句（试着把「平均耗时」调低）"
      >
        <el-table-column type="expand">
          <template #default="{ row }">
            <div style="padding: 4px 16px 12px">
              <pre class="sql-box mono">{{ row.sql }}</pre>
              <div class="row" style="gap: 14px; margin-top: 8px; font-size: 12px">
                <span class="muted">首次出现 {{ row.first_seen || '-' }}</span>
                <span class="muted">最后出现 {{ row.last_seen || '-' }}</span>
                <span class="muted">临时磁盘表 {{ fmtNum(row.tmp_disk_tables) }}</span>
                <div class="spacer"></div>
                <el-button size="small" @click="copySQL(row.sql)">复制 SQL</el-button>
                <el-button
                  size="small"
                  :disabled="row.sql.includes('?')"
                  :title="row.sql.includes('?') ? '语句模板含 ?，需替换参数后才能分析' : ''"
                  @click="explain(row.sql)"
                >执行计划</el-button>
              </div>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="语句" min-width="300">
          <template #default="{ row }">
            <span class="mono sql-cell">{{ row.sql }}</span>
          </template>
        </el-table-column>
        <el-table-column label="库" width="110">
          <template #default="{ row }">
            <span class="mono" style="font-size: 12px">{{ row.schema || '-' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="次数" width="86" align="right">
          <template #default="{ row }"><span class="mono">{{ fmtNum(row.count) }}</span></template>
        </el-table-column>
        <el-table-column label="平均" width="92" align="right">
          <template #default="{ row }">
            <span class="mono" :class="row.avg_ms >= 500 ? 'slow' : ''">{{ fmtMS(row.avg_ms) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="最大" width="92" align="right">
          <template #default="{ row }"><span class="mono">{{ fmtMS(row.max_ms) }}</span></template>
        </el-table-column>
        <el-table-column label="总耗时" width="100" align="right">
          <template #default="{ row }"><span class="mono">{{ fmtMS(row.total_ms) }}</span></template>
        </el-table-column>
        <el-table-column label="平均扫描行" width="110" align="right">
          <template #default="{ row }">
            <span class="mono" :class="row.avg_examined >= 10000 ? 'slow' : ''">{{ fmtNum(Math.round(row.avg_examined)) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="无索引" width="86" align="right">
          <template #default="{ row }">
            <span class="mono" :class="row.no_index_used ? 'slow' : 'muted'">{{ fmtNum(row.no_index_used) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="最后出现" width="150">
          <template #default="{ row }">
            <span class="muted mono" style="font-size: 11.5px">{{ row.last_seen || '-' }}</span>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 最近慢日志 -->
    <div class="card" style="padding: 0; margin-top: 12px">
      <div class="toolbar" style="padding: 12px 14px 8px">
        <span class="card-title">最近慢日志</span>
        <span class="muted" style="font-size: 12px; margin-left: 8px">
          来自 <code class="mono">mysql.slow_log</code>，仅当 log_output 含 TABLE 时有内容
        </span>
        <div class="spacer"></div>
        <span class="muted" style="font-size: 12px">{{ report.recent?.length || 0 }} 条</span>
      </div>
      <el-alert
        v-if="report.recent_error"
        type="info"
        :closable="false"
        show-icon
        style="margin: 0 14px 12px"
        :title="report.recent_error"
      />
      <el-table :data="report.recent || []" size="small" max-height="320" empty-text="没有慢日志记录">
        <el-table-column label="时间" width="150">
          <template #default="{ row }"><span class="mono" style="font-size: 11.5px">{{ row.start_time }}</span></template>
        </el-table-column>
        <el-table-column label="耗时" width="90" align="right">
          <template #default="{ row }"><span class="mono slow">{{ row.query_time?.toFixed(3) }} s</span></template>
        </el-table-column>
        <el-table-column label="锁等待" width="86" align="right">
          <template #default="{ row }"><span class="mono">{{ row.lock_time?.toFixed(3) }}</span></template>
        </el-table-column>
        <el-table-column label="返回/扫描" width="110" align="right">
          <template #default="{ row }">
            <span class="mono">{{ fmtNum(row.rows_sent) }}/{{ fmtNum(row.rows_examined) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="库" width="100">
          <template #default="{ row }"><span class="mono" style="font-size: 12px">{{ row.db || '-' }}</span></template>
        </el-table-column>
        <el-table-column label="SQL" min-width="260">
          <template #default="{ row }">
            <span class="mono" style="font-size: 11.5px; word-break: break-all">{{ row.sql }}</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="90" align="right">
          <template #default="{ row }">
            <el-button link type="primary" size="small" @click="explain(row.sql)">执行计划</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- EXPLAIN 结果 -->
    <el-dialog v-model="explainVisible" title="执行计划" width="860px">
      <div v-loading="explainLoading">
        <div class="muted" style="font-size: 12px; margin-bottom: 8px; word-break: break-all">
          {{ explainSQL }}
        </div>
        <el-table v-if="explainResult" :data="explainResult.rows" size="small" border max-height="420">
          <el-table-column v-for="(c, ci) in explainResult.columns" :key="c + ci" :label="c" min-width="120">
            <template #default="{ row }">
              <span class="mono" style="font-size: 12px">{{ row[ci] === null ? 'NULL' : row[ci] }}</span>
            </template>
          </el-table-column>
        </el-table>
        <div v-if="explainError" class="err-box mono">{{ explainError }}</div>
      </div>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import { api, errMsg } from '../api'
import { copyText } from '../utils'

const props = defineProps<{
  schema: string
  databases: any[]
}>()

const report = ref<any>({ available: false, settings: {}, items: [], recent: [] })
const loading = ref(false)
const filters = reactive({ schema: '', min_ms: 0, order: 'avg', limit: 50 })

const explainVisible = ref(false)
const explainLoading = ref(false)
const explainSQL = ref('')
const explainError = ref('')
const explainResult = ref<any>(null)

const settings = computed(() => report.value?.settings || {})
const psOn = computed(() => String(settings.value.performance_schema || '').toUpperCase() === 'ON')
const slowLogOn = computed(() => String(settings.value.slow_query_log || '').toUpperCase() === 'ON')

function fmtNum(n: any) {
  const v = Number(n || 0)
  return Number.isFinite(v) ? v.toLocaleString('zh-CN') : '-'
}

function fmtMS(v: any) {
  const n = Number(v || 0)
  if (!Number.isFinite(n) || n <= 0) return '0'
  if (n < 1) return n.toFixed(3)
  if (n < 1000) return n.toFixed(1)
  return (n / 1000).toFixed(2) + ' s'
}

async function load() {
  loading.value = true
  try {
    report.value = await api<any>('/api/mysql/slow', {
      query: {
        schema: filters.schema,
        min_ms: filters.min_ms,
        order: filters.order,
        limit: filters.limit
      }
    })
  } catch (e) {
    report.value = { available: false, error: errMsg(e), settings: report.value?.settings || {}, items: [], recent: [] }
  } finally {
    loading.value = false
  }
}

async function copySQL(sql: string) {
  const ok = await copyText(sql)
  if (ok) ElMessage.success('已复制')
  else ElMessage.error('复制失败，请手动选择文本')
}

async function explain(sql: string) {
  if (!sql || sql.includes('?')) {
    ElMessage.warning('语句模板含 ?，请把参数替换成实际值后再分析')
    return
  }
  explainSQL.value = sql
  explainVisible.value = true
  explainLoading.value = true
  explainError.value = ''
  explainResult.value = null
  try {
    explainResult.value = await api<any>('/api/mysql/slow/explain', {
      method: 'POST',
      body: { sql }
    })
  } catch (e) {
    explainError.value = errMsg(e)
  } finally {
    explainLoading.value = false
  }
}

// 跟随顶部切库；用户在下拉里选「全部」后不会被反复覆盖
watch(
  () => props.schema,
  (v) => {
    filters.schema = v || ''
  },
  { immediate: true }
)

onMounted(load)
</script>

<style scoped>
.sql-cell {
  font-size: 12px;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  word-break: break-all;
}
.sql-box {
  background: #f8f9fc;
  border: 1px solid var(--line);
  border-radius: 8px;
  padding: 10px 12px;
  font-size: 12px;
  line-height: 1.7;
  white-space: pre-wrap;
  word-break: break-all;
  margin: 0;
  max-height: 220px;
  overflow: auto;
}
.slow {
  color: #b25a00;
  font-weight: 600;
}
.err-box {
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
