<template>
  <div>
    <div class="toolbar">
      <el-input v-model="q" placeholder="搜索用户 / 操作 / 路径" clearable style="width: 260px" @input="onSearch">
        <template #prefix><el-icon><Search /></el-icon></template>
      </el-input>
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <div class="spacer"></div>
      <span class="muted" style="font-size: 11.5px">仅记录写操作（新增/修改/删除），保留最近 30 天</span>
    </div>

    <div class="card" style="padding: 0">
      <el-table :data="rows" v-loading="loading" size="default">
        <el-table-column label="时间" width="170">
          <template #default="{ row }"><span class="muted">{{ fmtTime(row.created_at) }}</span></template>
        </el-table-column>
        <el-table-column prop="username" label="操作人" width="120" />
        <el-table-column label="操作" min-width="230">
          <template #default="{ row }"><span class="mono" style="font-size: 12px">{{ row.action }}</span></template>
        </el-table-column>
        <el-table-column label="目标" min-width="200" show-overflow-tooltip>
          <template #default="{ row }"><span class="mono muted" style="font-size: 11.5px">{{ row.target }}</span></template>
        </el-table-column>
        <el-table-column label="详情" min-width="200" show-overflow-tooltip>
          <template #default="{ row }"><span class="muted" style="font-size: 12px">{{ row.detail }}</span></template>
        </el-table-column>
        <el-table-column label="IP" width="130">
          <template #default="{ row }"><span class="mono muted" style="font-size: 11.5px">{{ row.ip }}</span></template>
        </el-table-column>
        <el-table-column label="结果" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="row.result === 'success' ? 'success' : 'danger'">
              {{ row.result === 'success' ? '成功' : '失败' }}
            </el-tag>
          </template>
        </el-table-column>
      </el-table>
      <div style="padding: 12px 16px; display: flex; justify-content: flex-end">
        <el-pagination
          layout="total, prev, pager, next"
          :total="total"
          :page-size="size"
          :current-page="page"
          @current-change="onPage"
        />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { Refresh, Search } from '@element-plus/icons-vue'
import { api, errMsg } from '../api'
import { fmtTime } from '../utils'

const rows = ref<any[]>([])
const total = ref(0)
const page = ref(1)
const size = ref(20)
const q = ref('')
const loading = ref(false)
let timer: any = null

async function load() {
  loading.value = true
  try {
    const d = await api<any>('/api/audit', { query: { page: page.value, size: size.value, q: q.value } })
    rows.value = d.items || []
    total.value = d.total || 0
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

function onSearch() {
  clearTimeout(timer)
  timer = setTimeout(() => {
    page.value = 1
    load()
  }, 300)
}

function onPage(p: number) {
  page.value = p
  load()
}

onMounted(load)
</script>
