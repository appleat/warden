<template>
  <div class="fb" :class="{ compact }">
    <!-- 全宽工具栏（文件管理页） -->
    <div v-if="!compact" class="toolbar">
      <el-button :icon="Back" size="default" :disabled="atRoot" @click="up">上一级</el-button>
      <el-breadcrumb separator="/" style="flex: 1; min-width: 0">
        <el-breadcrumb-item v-for="(seg, i) in crumbs" :key="i">
          <a style="cursor: pointer" @click="jump(i)">{{ seg.label }}</a>
        </el-breadcrumb-item>
      </el-breadcrumb>
      <el-button :icon="Refresh" @click="load()">刷新</el-button>
      <el-button :icon="FolderAdd" @click="openMkdir">新建文件夹</el-button>
      <el-button :icon="DocumentAdd" @click="openTouch">新建文件</el-button>
      <el-button type="primary" :icon="Upload" :loading="uploading" @click="pick">上传</el-button>
    </div>

    <!-- 紧凑工具栏（终端右侧滑入面板） -->
    <template v-else>
      <div class="fb-bar">
        <el-button :icon="Back" size="small" :disabled="atRoot" title="上一级" @click="up" />
        <div class="fb-crumbs">
          <el-breadcrumb separator="/">
            <el-breadcrumb-item v-for="(seg, i) in crumbs" :key="i">
              <a style="cursor: pointer" @click="jump(i)">{{ seg.label }}</a>
            </el-breadcrumb-item>
          </el-breadcrumb>
        </div>
        <el-button :icon="Refresh" size="small" title="刷新" @click="load()" />
      </div>
      <div class="fb-bar">
        <el-button :icon="FolderAdd" size="small" title="新建文件夹" @click="openMkdir" />
        <el-button :icon="DocumentAdd" size="small" title="新建文件" @click="openTouch" />
        <el-button :icon="Upload" size="small" type="primary" :loading="uploading" title="上传文件" @click="pick" />
        <div class="spacer"></div>
        <span class="muted fb-count">{{ rows.length }} 项</span>
      </div>
      <el-progress v-if="uploading" :percentage="uploadPct" :stroke-width="4" style="margin: 0 0 6px" />
    </template>

    <el-progress v-if="uploading && !compact" :percentage="uploadPct" :stroke-width="6" style="margin-bottom: 12px" />

    <input ref="fileInput" type="file" multiple style="display: none" @change="onFiles" />

    <div :class="compact ? 'fb-table-wrap' : 'card'" :style="compact ? undefined : 'padding: 0'">
      <el-table
        :data="rows"
        v-loading="loading"
        :size="compact ? 'small' : 'default'"
        :height="compact ? '100%' : undefined"
        :empty-text="compact ? '空目录' : '空目录'"
        :class="compact ? 'fb-table' : ''"
        @row-dblclick="enter"
        @row-click="onRowClick"
      >
        <el-table-column :label="'名称'" :min-width="compact ? 150 : 320">
          <template #default="{ row }">
            <div class="row" style="gap: 8px; min-width: 0">
              <el-icon :style="{ color: row.dir ? '#e6a23c' : '#8b94a7', flex: '0 0 auto' }">
                <Folder v-if="row.dir" />
                <Document v-else />
              </el-icon>
              <a v-if="row.dir" class="fb-name" style="cursor: pointer; font-weight: 500" @click="enter(row)">
                {{ row.name }}
              </a>
              <span v-else class="fb-name" style="cursor: pointer" @click="openEdit(row)">{{ row.name }}</span>
              <el-tag v-if="row.link" size="small" type="info">链接</el-tag>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="大小" :width="compact ? 78 : 110">
          <template #default="{ row }">
            <span class="muted">{{ row.dir ? '-' : fmtBytes(row.size) }}</span>
          </template>
        </el-table-column>

        <el-table-column v-if="!compact" label="权限" width="110">
          <template #default="{ row }"><span class="mono muted">{{ row.mode }}</span></template>
        </el-table-column>
        <el-table-column v-if="!compact" label="修改时间" width="170">
          <template #default="{ row }"><span class="muted">{{ fmtTime(row.mod_time) }}</span></template>
        </el-table-column>

        <el-table-column label="操作" :width="compact ? 76 : 250" :align="'right'">
          <template #default="{ row }">
            <template v-if="!compact">
              <el-button v-if="!row.dir" link type="primary" size="small" @click="download(row)">下载</el-button>
              <el-button v-if="!row.dir" link type="primary" size="small" @click="openEdit(row)">编辑</el-button>
            </template>
            <el-button
              v-if="compact && !row.dir"
              link
              type="primary"
              size="small"
              :icon="Download"
              title="下载"
              @click.stop="download(row)"
            />
            <el-dropdown trigger="click" @command="(c: string) => onCommand(c, row)" style="vertical-align: middle; margin-left: 10px;">
              <el-button link type="primary" :size="compact ? 'small' : 'small'" @click.stop>{{ compact ? '···' : '更多' }}</el-button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item v-if="compact && !row.dir" command="download">下载</el-dropdown-item>
                  <el-dropdown-item v-if="compact && !row.dir" command="edit">编辑</el-dropdown-item>
                  <el-dropdown-item command="rename">重命名</el-dropdown-item>
                  <el-dropdown-item command="copy">复制副本</el-dropdown-item>
                  <el-dropdown-item command="chmod">修改权限</el-dropdown-item>
                  <el-dropdown-item command="compress">压缩为 zip</el-dropdown-item>
                  <el-dropdown-item v-if="isArchive(row.name)" command="extract">解压到当前目录</el-dropdown-item>
                  <el-dropdown-item command="delete" divided>删除</el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <el-dialog v-model="editorVisible" :title="'编辑 · ' + editing.name" :width="compact ? '92%' : '820px'" top="6vh" append-to-body>
      <el-input v-model="editing.content" type="textarea" :rows="compact ? 16 : 22" class="mono" resize="vertical" />
      <template #footer>
        <span class="muted" style="float: left; font-size: 11.5px; line-height: 32px">
          {{ fmtBytes(editing.size) }} · 仅支持文本文件，超过 4MB 请下载编辑
        </span>
        <el-button @click="editorVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="saveFile">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="nameVisible" :title="nameTitle" width="420px" append-to-body>
      <el-input v-model="nameValue" placeholder="名称" @keyup.enter="submitName" />
      <template #footer>
        <el-button @click="nameVisible = false">取消</el-button>
        <el-button type="primary" @click="submitName">确定</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  Back,
  Refresh,
  FolderAdd,
  DocumentAdd,
  Upload,
  Download,
  Folder,
  Document
} from '@element-plus/icons-vue'
import { api, errMsg, getToken } from '../api'
import { activeHostId } from '../hosts'
import { fmtBytes, fmtTime } from '../utils'

const props = withDefaults(defineProps<{ compact?: boolean; startPath?: string; hostId?: number }>(), {
  compact: false,
  startPath: '',
  hostId: 0
})
const emit = defineEmits<{ (e: 'path-change', v: string): void }>()

// 远程模式（hostId>0）下把 SSH 主机 id 随请求带上，后端据此走 SFTP；
// 本地模式不加任何 host 参数，避免被 HostProxy 转发到远程 Agent。
function scope(q?: Record<string, any>, b?: any) {
  if (!props.hostId) return { query: q, body: b }
  return {
    query: { ...(q || {}), id: props.hostId },
    // 仅在调用方显式提供 body 时才附加 id；否则 body 非空会让 api() 把 GET 误判成 POST
    body: b === undefined ? undefined : { ...b, id: props.hostId }
  }
}

const path = ref('')
const root = ref('')
const parent = ref('')
const rows = ref<any[]>([])
const loading = ref(false)
const uploading = ref(false)
const uploadPct = ref(0)
const fileInput = ref<HTMLInputElement>()

const editorVisible = ref(false)
const saving = ref(false)
const editing = reactive<any>({ path: '', name: '', content: '', size: 0 })

const nameVisible = ref(false)
const nameTitle = ref('')
const nameValue = ref('')
let nameAction: (v: string) => void = () => {}

// 统一路径分隔符，并把 Windows 的 "D:" 补成 "D:/"，避免拼出 "D:/D:" 这类非法路径
function normPath(p: string) {
  let s = (p || '').trim().replace(/\\/g, '/')
  if (/^[a-zA-Z]:$/.test(s)) s += '/'
  return s
}

// 无法从后端拿到 parent 时的兜底计算：/a/b -> /a，D:/a -> D:/，/a -> /
function parentOf(p: string) {
  const s = normPath(p).replace(/\/+$/, '')
  if (!s) return ''
  if (/^[a-zA-Z]:$/.test(s)) return s + '/'
  const i = s.lastIndexOf('/')
  if (i < 0) return ''
  const head = s.slice(0, i)
  return /^[a-zA-Z]:$/.test(head) ? head + '/' : head || '/'
}

const atRoot = computed(() => {
  const cur = normPath(path.value)
  const rt = normPath(root.value)
  if (!cur) return true
  return !!rt && cur.replace(/\/+$/, '') === rt.replace(/\/+$/, '')
})

const crumbs = computed(() => {
  const p = normPath(path.value)
  const out: any[] = []
  const drive = /^([a-zA-Z]:)\//.exec(p)
  const unixRoot = !drive && p.startsWith('/')
  let acc = ''
  if (drive) {
    acc = drive[1]
    out.push({ label: drive[1], path: drive[1] + '/' })
  } else if (unixRoot) {
    out.push({ label: '根目录', path: '/' })
  }
  const rest = drive ? p.slice(2) : p
  for (const seg of rest.split('/').filter(Boolean)) {
    if (drive) acc = `${acc}/${seg}`
    else if (unixRoot) acc = `${acc}/${seg}`
    else acc = acc ? `${acc}/${seg}` : seg
    out.push({ label: seg, path: acc })
  }
  return out
})

async function load(p?: string) {
  loading.value = true
  try {
    const d = await api<any>('/api/files', scope({ path: p ?? path.value }))
    path.value = d.path
    root.value = d.root || ''
    parent.value = d.parent || ''
    rows.value = d.items || []
    emit('path-change', d.path)
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

function enter(row: any) {
  if (row.dir) load(row.path)
}

// 紧凑面板里单击目录即进入，避免小尺寸下还要双击
function onRowClick(row: any) {
  if (props.compact && row.dir) load(row.path)
}

function up() {
  if (atRoot.value) return
  const target = parent.value || parentOf(path.value) || root.value
  load(target)
}

function jump(i: number) {
  const c = crumbs.value[i]
  if (!c) return
  load(c.path)
}

function isArchive(name: string) {
  return /\.(zip|tar|tar\.gz|tgz)$/i.test(name)
}

async function download(row: any) {
  const idq = props.hostId ? `id=${props.hostId}&` : ''
  const url = `/api/files/download?${idq}path=${encodeURIComponent(row.path)}&token=${getToken()}`
  const a = document.createElement('a')
  a.href = url
  a.download = row.name
  document.body.appendChild(a)
  a.click()
  a.remove()
}

function pick() {
  fileInput.value?.click()
}

async function onFiles(e: Event) {
  const files = (e.target as HTMLInputElement).files
  if (!files || !files.length) return
  const fd = new FormData()
  for (const f of Array.from(files)) fd.append('file', f, f.name)

  uploading.value = true
  uploadPct.value = 0
  await new Promise<void>((resolve) => {
    const xhr = new XMLHttpRequest()
    xhr.open('POST', `/api/files/upload?${props.hostId ? 'id=' + props.hostId + '&' : ''}path=${encodeURIComponent(path.value)}`)
    xhr.setRequestHeader('Authorization', `Bearer ${getToken()}`)
    xhr.upload.onprogress = (ev) => {
      if (ev.lengthComputable) uploadPct.value = Math.round((ev.loaded / ev.total) * 100)
    }
    xhr.onload = () => {
      try {
        const j = JSON.parse(xhr.responseText)
        if (j.code === 0) ElMessage.success(`已上传 ${j.data.items.length} 个文件`)
        else ElMessage.error(j.msg || '上传失败')
      } catch {
        ElMessage.error('上传失败')
      }
      resolve()
    }
    xhr.onerror = () => {
      ElMessage.error('上传失败')
      resolve()
    }
    xhr.send(fd)
  })
  uploading.value = false
  if (fileInput.value) fileInput.value.value = ''
  load()
}

function openMkdir() {
  nameTitle.value = '新建文件夹'
  nameValue.value = ''
  nameAction = async (v: string) => {
    await api('/api/files/mkdir', scope(undefined, { path: path.value, name: v }))
    ElMessage.success('已创建')
    load()
  }
  nameVisible.value = true
}

function openTouch() {
  nameTitle.value = '新建文件'
  nameValue.value = ''
  nameAction = async (v: string) => {
    await api('/api/files/touch', scope(undefined, { path: path.value, name: v }))
    ElMessage.success('已创建')
    load()
  }
  nameVisible.value = true
}

async function submitName() {
  if (!nameValue.value.trim()) return
  try {
    await nameAction(nameValue.value.trim())
    nameVisible.value = false
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function openEdit(row: any) {
  try {
    const d = await api<any>('/api/files/content', scope({ path: row.path }))
    editing.path = row.path
    editing.name = row.name
    editing.content = d.content
    editing.size = d.size
    editorVisible.value = true
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

async function saveFile() {
  saving.value = true
  try {
    await api('/api/files/content', { method: 'PUT', ...scope(undefined, { path: editing.path, content: editing.content }) })
    ElMessage.success('已保存')
    editorVisible.value = false
    load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
  }
}

async function onCommand(cmd: string, row: any) {
  try {
    if (cmd === 'download') return download(row)
    if (cmd === 'edit') return openEdit(row)
    if (cmd === 'rename') {
      nameTitle.value = '重命名为'
      nameValue.value = row.name
      nameAction = async (v: string) => {
        await api('/api/files/rename', scope(undefined, { from: row.path, to: v }))
        load()
      }
      nameVisible.value = true
      return
    }
    if (cmd === 'copy') {
      nameTitle.value = '复制为（留空自动命名）'
      nameValue.value = row.name + '.copy'
      nameAction = async (v: string) => {
        await api('/api/files/copy', scope(undefined, { from: row.path, to: v }))
        load()
      }
      nameVisible.value = true
      return
    }
    if (cmd === 'chmod') {
      const { value } = await ElMessageBox.prompt('输入八进制权限，例如 755', '修改权限', {
        inputValue: '644',
        inputPattern: /^[0-7]{3,4}$/,
        inputErrorMessage: '请输入 3~4 位八进制数'
      })
      await api('/api/files/chmod', scope(undefined, { path: row.path, mode: value }))
      ElMessage.success('权限已修改')
      load()
      return
    }
    if (cmd === 'compress') {
      const r = await api<any>('/api/files/compress', scope(undefined, { path: row.path, format: 'zip' }))
      ElMessage.success('已压缩：' + r.path.split('/').pop())
      load()
      return
    }
    if (cmd === 'extract') {
      await api('/api/files/extract', scope(undefined, { path: row.path, dest: path.value }))
      ElMessage.success('已解压')
      load()
      return
    }
    if (cmd === 'delete') {
      await ElMessageBox.confirm(`确定删除「${row.name}」吗？目录将被递归删除。`, '删除', {
        type: 'warning',
        confirmButtonClass: 'el-button--danger'
      })
      await api('/api/files', { method: 'DELETE', query: props.hostId ? { path: row.path, id: props.hostId } : { path: row.path } })
      ElMessage.success('已删除')
      load()
    }
  } catch (e: any) {
    if (e === 'cancel' || e === 'close') return
    ElMessage.error(errMsg(e))
  }
}

onMounted(() => load(props.startPath || ''))

// 顶部切换主机后，面板里的目录必须跟着换一台机器，否则会对着旧主机操作
watch(activeHostId, () => load(''))
// 远程/本机切换或换一台 SSH 主机时，文件面板重置到对应机器的根目录
watch(() => props.hostId, () => load(''))

defineExpose({ reload: () => load(), currentPath: () => path.value })
</script>

<style scoped>
.fb {
  display: flex;
  flex-direction: column;
  min-height: 0;
}
.fb.compact {
  height: 100%;
}
.fb-bar {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 8px;
  min-width: 0;
}
.fb-bar :deep(.el-button + .el-button) {
  margin-left: 0;
}
.fb-crumbs {
  flex: 1;
  min-width: 0;
  overflow: hidden auto;
  white-space: nowrap;
  font-size: 12.5px;
  padding: 0 2px;
}
.fb-crumbs :deep(.el-breadcrumb) {
  font-size: 12.5px;
  white-space: nowrap;
}
.fb-count {
  font-size: 11.5px;
  white-space: nowrap;
}
.fb-table-wrap {
  flex: 1;
  min-height: 0;
  overflow: hidden;
}
.fb-table {
  font-size: 12.5px;
}
.fb-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
::deep(.el-textarea__inner) {
  font-family: 'SFMono-Regular', Consolas, Menlo, monospace;
  font-size: 12.5px;
  line-height: 1.7;
}
</style>
