<template>
  <div>
    <el-tabs v-model="tab">
      <!-- SSH 主机 -->
      <el-tab-pane label="SSH 主机" name="ssh">
        <div class="toolbar">
          <el-button type="primary" :icon="Plus" @click="openHost()">添加主机</el-button>
          <div class="spacer"></div>
          <span class="muted" style="font-size: 12px">凭据使用 AES-256-GCM 加密后存储</span>
        </div>
        <div class="card" style="padding: 0">
          <el-table :data="hosts" size="default" v-loading="hostLoading" empty-text="还没有添加远程主机">
            <el-table-column prop="name" label="名称" min-width="140" />
            <el-table-column label="地址" min-width="200">
              <template #default="{ row }"><span class="mono">{{ row.username }}@{{ row.host }}:{{ row.port }}</span></template>
            </el-table-column>
            <el-table-column label="认证" width="100">
              <template #default="{ row }">
                <el-tag size="small" effect="plain">{{ row.auth_type === 'key' ? '私钥' : '密码' }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="remark" label="备注" min-width="140" show-overflow-tooltip />
            <el-table-column label="操作" width="240" align="right">
              <template #default="{ row }">
                <el-button link type="primary" size="small" @click="testHost(row)">测试</el-button>
                <el-button link type="primary" size="small" @click="connectHost(row)">终端</el-button>
                <el-button link type="primary" size="small" @click="openHost(row)">编辑</el-button>
                <el-button link type="danger" size="small" @click="delHost(row)">删除</el-button>
              </template>
            </el-table-column>
          </el-table>
        </div>
      </el-tab-pane>

      <!-- 终端 -->
      <el-tab-pane label="终端" name="terminal">
        <div class="toolbar">
          <el-radio-group v-model="termMode" size="small" @change="resetTerm">
            <el-radio-button label="local">本机终端</el-radio-button>
            <el-radio-button label="ssh">远程 SSH</el-radio-button>
          </el-radio-group>
          <el-select
            v-if="termMode === 'ssh'"
            v-model="termHost"
            placeholder="选择主机"
            size="small"
            style="width: 220px"
            @change="resetTerm"
          >
            <el-option v-for="h in hosts" :key="h.id" :label="`${h.name} · ${h.host}`" :value="h.id" />
          </el-select>
          <div class="spacer"></div>
          <span class="muted" style="font-size: 11.5px">支持 Ctrl+C / vim / htop 等全交互程序</span>
          <el-button
            size="small"
            :type="filesOpen ? 'primary' : 'default'"
            :icon="Folder"
            :title="filesOpen ? '收起文件面板' : '在右侧打开文件面板'"
            @click="filesOpen = !filesOpen"
          >
            {{ filesOpen ? '收起文件' : '文件面板' }}
          </el-button>
        </div>

        <div class="term-split">
          <div class="term-main">
            <XTerm
              v-if="termUrl"
              ref="termRef"
              :key="termKey"
              :url="termUrl"
              :title="termMode === 'local' ? '本机 Shell' : 'SSH 会话'"
            />
            <div v-else class="card empty-hint">请选择一台主机后打开终端</div>
          </div>

          <transition name="fp-slide" @after-enter="refitTerm">
            <div v-if="filesOpen" class="file-panel" :style="{ '--fp-width': fileWidth + 'px' }">
              <!-- 拖拽调宽 -->
              <div class="file-resizer" title="拖动调整宽度" @mousedown="startResize"></div>
              <div class="file-panel-inner">
                <div class="file-panel-head">
                  <span class="fp-title">文件管理</span>
                  <el-tag size="small" effect="plain" :title="termMode === 'ssh' ? '文件操作跟随所选远程主机' : '文件操作跟随本机'">{{ fileHostLabel }}</el-tag>
                  <div class="spacer"></div>
                  <el-button link :icon="Close" title="收起" @click="filesOpen = false" />
                </div>
                <div class="file-panel-body">
                  <FileBrowser compact :start-path="fbPath" :host-id="termMode === 'ssh' && termHost ? termHost : 0" @path-change="fbPath = $event" />
                </div>
              </div>
            </div>
          </transition>
        </div>
      </el-tab-pane>
    </el-tabs>

    <el-dialog v-model="hostVisible" :title="hostForm.id ? '编辑主机' : '添加主机'" width="540px">
      <el-form :model="hostForm" label-width="86px">
        <el-form-item label="名称" required><el-input v-model="hostForm.name" placeholder="生产环境-1" /></el-form-item>
        <el-form-item label="地址" required>
          <div class="row" style="width: 100%">
            <el-input v-model="hostForm.host" placeholder="192.168.1.10" style="flex: 1" />
            <el-input v-model.number="hostForm.port" placeholder="22" style="width: 90px" />
          </div>
        </el-form-item>
        <el-form-item label="用户名" required><el-input v-model="hostForm.username" placeholder="root" /></el-form-item>
        <el-form-item label="认证方式">
          <el-radio-group v-model="hostForm.auth_type">
            <el-radio-button label="password">密码</el-radio-button>
            <el-radio-button label="key">私钥</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item :label="hostForm.auth_type === 'key' ? '私钥内容' : '密码'">
          <el-input
            v-model="hostForm.secret"
            :type="hostForm.auth_type === 'key' ? 'textarea' : 'password'"
            :rows="6"
            :placeholder="
              hostForm.id
                ? '留空表示不修改'
                : hostForm.auth_type === 'key'
                  ? '-----BEGIN OPENSSH PRIVATE KEY-----'
                  : '登录密码'
            "
            show-password
          />
        </el-form-item>
        <el-form-item v-if="hostForm.auth_type === 'key'" label="私钥口令">
          <el-input v-model="hostForm.passphrase" type="password" show-password placeholder="可选" />
        </el-form-item>
        <el-form-item label="备注"><el-input v-model="hostForm.remark" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="hostVisible = false">取消</el-button>
        <el-button type="primary" :loading="hostSaving" @click="saveHost">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Folder, Close } from '@element-plus/icons-vue'
import XTerm from '../components/XTerm.vue'
import FileBrowser from '../components/FileBrowser.vue'
import { api, errMsg, wsURL } from '../api'

const route = useRoute()
const router = useRouter()

// 页签由路由决定：导航里的「SSH 终端管理」直接落到终端页签
function tabOf(p: string) {
  return p.endsWith('/ssh') ? 'ssh' : 'terminal'
}
const tab = ref(tabOf(route.path))
watch(() => route.path, (p) => {
  tab.value = tabOf(p)
})
// 反向同步：手动点页签时也把地址栏和左侧高亮带过去（replace，不堆历史）
watch(tab, (v) => {
  const want = v === 'ssh' ? '/system/ssh' : '/system/terminal'
  if (route.path !== want) router.replace(want)
})

const hosts = ref<any[]>([])
const hostLoading = ref(false)
const hostVisible = ref(false)
const hostSaving = ref(false)
const hostForm = reactive<any>({
  id: 0,
  name: '',
  host: '',
  port: 22,
  username: 'root',
  auth_type: 'password',
  secret: '',
  passphrase: '',
  remark: ''
})

const termMode = ref('local')
const termHost = ref<number | null>(null)
const termKey = ref(0)
const termUrl = computed(() => {
  if (termMode.value === 'local') return wsURL('/ws/terminal/local')
  if (!termHost.value) return ''
  return wsURL('/ws/terminal/ssh', { id: termHost.value })
})

function resetTerm() {
  termKey.value++
}

// 文件面板标题：跟随当前终端选中的主机（本机 / 远程 SSH）
const fileHostLabel = computed(() => {
  if (termMode.value === 'ssh' && termHost.value) {
    const h = hosts.value.find((x) => x.id === termHost.value)
    return h?.name || '远程主机'
  }
  return '本机'
})

// ---------- 终端右侧文件面板 ----------
const termRef = ref<any>(null)
const filesOpen = ref(localStorage.getItem('warden.termFiles') === '1')
const fbPath = ref('')
const fileWidth = ref(Number(localStorage.getItem('warden.termFilesWidth')) || 460)

watch(filesOpen, (v) => localStorage.setItem('warden.termFiles', v ? '1' : '0'))

// 窗口太窄时把面板宽度收回来，避免终端被挤成一条缝
// 上限 = 拆分区域宽度 - 终端最小宽度(420) - 间隔(12)
function maxPanelWidth() {
  const split = document.querySelector('.term-split') as HTMLElement | null
  const total = split?.clientWidth || window.innerWidth - 300
  return Math.max(320, Math.min(720, total - 432))
}

function clampFileWidth() {
  fileWidth.value = Math.min(Math.max(fileWidth.value, 320), maxPanelWidth())
}

function onWinResize() {
  clampFileWidth()
  refitTerm()
}

onMounted(() => {
  clampFileWidth()
  window.addEventListener('resize', onWinResize)
})

// 面板滑入/拖拽后终端宽度变了，主动让 xterm 重新 fit 一次（ResizeObserver 已有防抖兜底）
function refitTerm() {
  termRef.value?.refit?.()
}

function startResize(e: MouseEvent) {
  e.preventDefault()
  const startX = e.clientX
  const startW = fileWidth.value

  const onMove = (ev: MouseEvent) => {
    fileWidth.value = Math.round(Math.min(Math.max(startW + (startX - ev.clientX), 320), maxPanelWidth()))
  }
  const onUp = () => {
    window.removeEventListener('mousemove', onMove)
    window.removeEventListener('mouseup', onUp)
    document.body.style.userSelect = ''
    localStorage.setItem('warden.termFilesWidth', String(fileWidth.value))
    refitTerm()
  }
  document.body.style.userSelect = 'none'
  window.addEventListener('mousemove', onMove)
  window.addEventListener('mouseup', onUp)
}

async function loadHosts() {
  hostLoading.value = true
  try {
    hosts.value = (await api<any>('/api/ssh/hosts')).items || []
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    hostLoading.value = false
  }
}

function openHost(row?: any) {
  Object.assign(hostForm, {
    id: row?.id || 0,
    name: row?.name || '',
    host: row?.host || '',
    port: row?.port || 22,
    username: row?.username || 'root',
    auth_type: row?.auth_type || 'password',
    secret: '',
    passphrase: '',
    remark: row?.remark || ''
  })
  hostVisible.value = true
}

async function saveHost() {
  if (!hostForm.name || !hostForm.host || !hostForm.username) {
    return ElMessage.warning('请填写名称、地址和用户名')
  }
  hostSaving.value = true
  try {
    await api('/api/ssh/hosts', { body: { ...hostForm } })
    ElMessage.success('已保存')
    hostVisible.value = false
    loadHosts()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    hostSaving.value = false
  }
}

async function testHost(row: any) {
  const loading = ElMessage({ message: '正在连接…', duration: 0 })
  try {
    const res = await api<any>(`/api/ssh/hosts/${row.id}/test`, { method: 'POST' })
    ElMessageBox.alert(`<pre style="font-size:12px;line-height:1.6">${(res.output || '').replace(/</g, '&lt;')}</pre>`, '连接成功', {
      dangerouslyUseHTMLString: true
    })
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.close()
  }
}

function connectHost(row: any) {
  termMode.value = 'ssh'
  termHost.value = row.id
  tab.value = 'terminal'
  resetTerm()
}

async function delHost(row: any) {
  try {
    await ElMessageBox.confirm(`删除主机「${row.name}」？`, '删除', { type: 'warning' })
  } catch {
    return
  }
  try {
    await api(`/api/ssh/hosts/${row.id}`, { method: 'DELETE' })
    ElMessage.success('已删除')
    loadHosts()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

onMounted(() => {
  loadHosts()
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', onWinResize)
})
</script>
<style scoped>
/* ---------- 终端 + 右侧文件面板 ---------- */
.term-split {
  display: flex;
  height: calc(100vh - 250px);
  min-height: 360px;
}
.term-main {
  flex: 1;
  min-width: 0;
  height: 100%;
}

.file-panel {
  position: relative;
  flex: 0 0 auto;
  width: var(--fp-width);
  height: 100%;
  margin-left: 12px;
  background: var(--card);
  border: 1px solid var(--line);
  border-radius: 10px;
  overflow: hidden;
  transition:
    width 0.24s cubic-bezier(0.32, 0.72, 0.28, 1),
    margin-left 0.24s cubic-bezier(0.32, 0.72, 0.28, 1),
    opacity 0.18s ease;
}
/* 内容宽度固定，动画过程中只裁切、不重排 */
.file-panel-inner {
  display: flex;
  flex-direction: column;
  width: var(--fp-width);
  height: 100%;
}
.file-panel-head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 8px 8px 14px;
  border-bottom: 1px solid var(--line);
  flex: 0 0 auto;
}
.fp-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--ink);
}
.file-panel-body {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  padding: 10px 12px 8px;
}
.file-panel-body > * {
  flex: 1;
  min-height: 0;
}

/* 拖拽调宽 */
.file-resizer {
  position: absolute;
  top: 0;
  left: 0;
  width: 6px;
  height: 100%;
  cursor: col-resize;
  z-index: 3;
}
.file-resizer:hover {
  background: linear-gradient(90deg, rgba(59, 99, 232, 0.16), transparent);
}

/* 滑入动画：宽度从 0 展开 */
.fp-slide-enter-from,
.fp-slide-leave-to {
  width: 0;
  margin-left: 0;
  opacity: 0;
  border-width: 0;
}
.fp-slide-enter-active,
.fp-slide-leave-active {
  overflow: hidden;
}
</style>
