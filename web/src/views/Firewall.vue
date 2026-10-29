<template>
  <div>
    <el-alert
      v-if="revert.left > 0"
      type="warning"
      :closable="false"
      show-icon
      style="margin-bottom: 12px"
    >
      <template #title>
        规则已生效，但改动了访问策略 —— <b>{{ revert.left }} 秒</b> 内未确认将<b>自动回滚</b>，
        防止把自己锁在服务器外面。
      </template>
      <div style="margin-top: 8px">
        <el-button size="small" type="warning" @click="confirmRevert">我确认保留此次改动</el-button>
      </div>
    </el-alert>

    <div class="grid" style="grid-template-columns: 1fr 330px; align-items: start">
      <div class="card" style="padding: 0">
        <div class="card-title" style="padding: 14px 16px 0; margin-bottom: 6px">
          <div class="row">
            <span>规则列表</span>
            <el-tag size="small" effect="plain">{{ fw.driver || '-' }}</el-tag>
            <el-tag size="small" :type="fw.enabled ? 'success' : 'info'" effect="plain">
              {{ fw.enabled ? '已启用' : '未启用' }}
            </el-tag>
          </div>
          <el-button size="small" :icon="Refresh" @click="loadFw">刷新</el-button>
        </div>
        <el-table :data="fw.rules || []" size="small" v-loading="fwLoading" :empty-text="fw.message || '暂无规则'">
          <el-table-column prop="id" label="#" width="56" />
          <el-table-column label="动作" width="88">
            <template #default="{ row }">
              <el-tag size="small" :type="row.action === 'deny' ? 'danger' : row.action === 'allow' ? 'success' : 'info'">
                {{ row.action }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="端口" width="110">
            <template #default="{ row }"><span class="mono">{{ row.port || '全部' }}</span></template>
          </el-table-column>
          <el-table-column label="协议" width="80">
            <template #default="{ row }"><span class="muted">{{ row.proto }}</span></template>
          </el-table-column>
          <el-table-column label="来源" min-width="150">
            <template #default="{ row }"><span class="mono" style="font-size: 12px">{{ row.from }}</span></template>
          </el-table-column>
          <el-table-column label="原始规则" min-width="220" show-overflow-tooltip>
            <template #default="{ row }"><span class="mono muted" style="font-size: 11px">{{ row.raw }}</span></template>
          </el-table-column>
          <el-table-column label="操作" width="80" align="right">
            <template #default="{ row }">
              <el-button link type="danger" size="small" @click="delRule(row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>

      <div>
        <div class="card">
          <div class="card-title"><span>防火墙开关</span></div>
          <div class="row">
            <el-switch v-model="fw.enabled" :disabled="!fw.supported" @change="toggleFw" />
            <span class="muted" style="font-size: 12px">{{ fw.enabled ? '已启用（默认拒绝入站）' : '未启用（放行所有入站）' }}</span>
          </div>
          <div class="muted" style="font-size: 11.5px; margin-top: 10px; line-height: 1.7">
            {{ fw.supported ? `检测到 ${fw.driver}，权限${fw.can_edit ? '正常' : '不足（需 root）'}` : fw.message }}
          </div>
        </div>

        <div class="card" style="margin-top: 14px">
          <div class="card-title"><span>放行 / 封禁端口</span></div>
          <el-form label-position="top" size="default">
            <el-form-item label="动作">
              <el-radio-group v-model="rule.action">
                <el-radio-button label="allow">放行</el-radio-button>
                <el-radio-button label="deny">封禁</el-radio-button>
              </el-radio-group>
            </el-form-item>
            <el-form-item label="端口">
              <el-input v-model="rule.port" placeholder="如 8080，范围 8000:9000" />
            </el-form-item>
            <el-form-item label="协议">
              <el-select v-model="rule.proto" style="width: 100%">
                <el-option label="tcp" value="tcp" />
                <el-option label="udp" value="udp" />
                <el-option label="全部" value="any" />
              </el-select>
            </el-form-item>
            <el-form-item label="来源（留空为任意）">
              <el-input v-model="rule.from" placeholder="1.2.3.4 或 10.0.0.0/8" />
            </el-form-item>
            <el-form-item label="备注">
              <el-input v-model="rule.comment" placeholder="可选" maxlength="60" />
            </el-form-item>
            <el-button type="primary" style="width: 100%" :loading="ruleSaving" @click="addRule">
              应用规则
            </el-button>
          </el-form>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import { api, errMsg } from '../api'

const fw = ref<any>({ rules: [] })
const fwLoading = ref(false)
const ruleSaving = ref(false)
const rule = reactive({ action: 'allow', port: '', proto: 'tcp', from: '', comment: '' })
const revert = reactive({ token: '', left: 0 })
let revertTimer: any = null

async function loadFw() {
  fwLoading.value = true
  try {
    fw.value = await api('/api/system/firewall')
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    fwLoading.value = false
  }
}

// 改动访问策略后留一个确认窗口，超时自动回滚，避免把自己锁在服务器外
function startRevert(token: string, seconds: number) {
  clearInterval(revertTimer)
  revert.token = token
  revert.left = seconds
  revertTimer = setInterval(() => {
    revert.left--
    if (revert.left <= 0) {
      clearInterval(revertTimer)
      revert.token = ''
      ElMessage.warning('未确认，已自动回滚防火墙规则')
      loadFw()
    }
  }, 1000)
}

async function confirmRevert() {
  try {
    await api('/api/system/firewall/confirm', { body: { token: revert.token } })
    clearInterval(revertTimer)
    revert.left = 0
    revert.token = ''
    ElMessage.success('已确认保留')
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function addRule() {
  ruleSaving.value = true
  try {
    const res = await api<any>('/api/system/firewall/rules', { body: { ...rule } })
    ElMessage.success('规则已应用')
    if (res?.revert_token) startRevert(res.revert_token, res.revert_seconds || 30)
    rule.port = ''
    rule.from = ''
    rule.comment = ''
    loadFw()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    ruleSaving.value = false
  }
}

async function delRule(row: any) {
  try {
    await ElMessageBox.confirm(`删除规则 #${row.id}：${row.raw}`, '删除规则', { type: 'warning' })
  } catch {
    return
  }
  try {
    const res = await api<any>(`/api/system/firewall/rules/${row.id}`, { method: 'DELETE' })
    ElMessage.success('已删除')
    if (res?.revert_token) startRevert(res.revert_token, res.revert_seconds || 30)
    loadFw()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function toggleFw(val: any) {
  try {
    const res = await api<any>('/api/system/firewall/toggle', { body: { enabled: val } })
    ElMessage.success(val ? '防火墙已启用' : '防火墙已关闭')
    if (res?.revert_token) startRevert(res.revert_token, res.revert_seconds || 30)
    loadFw()
  } catch (e) {
    ElMessage.error(errMsg(e))
    loadFw()
  }
}

onMounted(loadFw)

onBeforeUnmount(() => {
  clearInterval(revertTimer)
})
</script>
