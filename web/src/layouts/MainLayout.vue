<template>
  <div class="layout">
    <aside class="sidebar">
      <div class="brand">
        <img class="brand-mark" :src="logoMark" alt="Warden" />
        <div>
          <div class="brand-text">Warden</div>
          <div class="brand-sub">Linux 服务器管理</div>
        </div>
      </div>

      <nav class="nav">
        <template v-for="group in navGroups" :key="group.title">
          <div class="nav-group">{{ group.title }}</div>
          <template v-for="item in group.items" :key="item.name">
            <!-- 有二级菜单的条目 -->
            <template v-if="item.children">
              <div
                class="nav-item nav-parent"
                :class="{ active: isGroupActive(item), collapsed: isCollapsed(item) }"
                @click="toggleGroup(item)"
              >
                <el-icon><component :is="item.icon" /></el-icon>
                <span>{{ item.name }}</span>
                <el-icon class="nav-arrow">
                  <ArrowRight v-if="isCollapsed(item)" />
                  <ArrowDown v-else />
                </el-icon>
              </div>
              <div v-show="!isCollapsed(item)" class="nav-sub">
                <div
                  v-for="child in item.children"
                  :key="child.path"
                  class="nav-item nav-sub-item"
                  :class="{ active: route.path === child.path }"
                  @click="go(child.path)"
                >
                  <span class="nav-sub-dot"></span>
                  <span>{{ child.name }}</span>
                </div>
              </div>
            </template>

            <div
              v-else
              class="nav-item"
              :class="{ active: route.path === item.path }"
              @click="go(item.path)"
            >
              <el-icon><component :is="item.icon" /></el-icon>
              <span>{{ item.name }}</span>
              <span v-if="item.path === '/hosts' && remoteHosts.length" class="nav-badge">
                {{ onlineCount }}/{{ hostList.length }}
              </span>
            </div>
          </template>
        </template>
      </nav>

      <div class="sidebar-foot">
        <span>v{{ version }}</span>
        <span>{{ activeHost?.name || '' }}</span>
      </div>
    </aside>

    <div class="main">
      <header class="topbar">
        <div>
          <div class="page-title">{{ route.meta.title || '控制台' }}</div>
          <div class="page-desc">{{ route.meta.desc || '' }}</div>
        </div>
        <div class="topbar-right">
          <el-select
            v-model="activeHostId"
            class="host-select"
            placeholder="选择主机"
            @change="onHostChange"
          >
            <template #prefix>
              <span class="host-dot" :class="activeHost?.online ? 'on' : 'off'"></span>
            </template>
            <el-option v-for="h in hostList" :key="h.id" :label="h.name" :value="h.id">
              <div class="host-opt">
                <span class="host-dot" :class="h.online ? 'on' : 'off'"></span>
                <span class="host-name">{{ h.name }}</span>
                <span class="host-tag">
                  {{ h.kind === 'local' ? '本机' : h.online ? h.latency_ms + ' ms' : '离线' }}
                </span>
              </div>
            </el-option>
          </el-select>
          <el-tooltip content="管理主机" placement="bottom">
            <el-button circle size="small" :icon="Connection" @click="go('/hosts')" />
          </el-tooltip>
          <el-tooltip content="刷新当前页" placement="bottom">
            <el-button circle size="small" :icon="Refresh" @click="refresh" />
          </el-tooltip>
          <el-dropdown trigger="click" @command="onCommand">
            <div class="row" style="cursor: pointer">
              <el-avatar :size="26" style="background: var(--brand); font-size: 12px">
                {{ (user?.username || 'U').slice(0, 1).toUpperCase() }}
              </el-avatar>
              <span style="font-size: 13px">{{ user?.username || '未登录' }}</span>
              <el-tag size="small" effect="plain" style="margin-left: 2px">{{ roleLabel }}</el-tag>
            </div>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="settings">账号设置</el-dropdown-item>
                <el-dropdown-item command="logout" divided>退出登录</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </header>

      <main class="content">
        <div v-if="offlineHint" class="offline-bar">
          <el-icon><WarningFilled /></el-icon>
          <span>{{ offlineHint }}</span>
          <el-button link type="primary" size="small" @click="reloadHosts">重新探测</el-button>
        </div>
        <router-view v-if="alive" :key="route.fullPath + '-' + activeHostId" />
      </main>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  Odometer,
  Box,
  Monitor,
  Setting,
  Aim,
  Document,
  Refresh,
  Connection,
  WarningFilled,
  ArrowDown,
  ArrowRight
} from '@element-plus/icons-vue'
import { api, setToken } from '../api'
import logoMark from '../assets/logo/warden-mark.svg'
import { activeHost, activeHostId, hostList, loadHosts, onlineCount, remoteHosts, switchHost } from '../hosts'

const route = useRoute()
const router = useRouter()

interface NavItem {
  name: string
  path?: string
  icon?: any
  children?: NavItem[]
}

const navGroups: { title: string; items: NavItem[] }[] = [
  {
    title: '概览',
    items: [{ name: '数据看板', path: '/dashboard', icon: Odometer }]
  },
  {
    title: '运维',
    items: [
      { name: '主机管理', path: '/hosts', icon: Connection },
      {
        name: '服务管理',
        icon: Box,
        children: [
          { name: '容器管理', path: '/containers' },
          { name: 'Nacos 服务', path: '/nacos' },
          { name: 'Redis 服务', path: '/redis' }
        ]
      },
      {
        name: '系统管理',
        icon: Monitor,
        children: [
          { name: '文件管理', path: '/files' },
          { name: '进程管理', path: '/system/processes' },
          { name: '防火墙管理', path: '/system/firewall' },
          { name: 'SSH 终端管理', path: '/system/terminal' }
        ]
      }
    ]
  },
  {
    title: '监控',
    items: [
      { name: '网站监控', path: '/monitor', icon: Aim },
      { name: '审计日志', path: '/audit', icon: Document }
    ]
  },
  {
    title: '系统',
    items: [{ name: '设置', path: '/settings', icon: Setting }]
  }
]

// 二级菜单折叠状态：默认展开，点击父级切换
const collapsed = ref<Record<string, boolean>>({})

function isCollapsed(item: NavItem) {
  return collapsed.value[item.name] === true
}

function toggleGroup(item: NavItem) {
  collapsed.value[item.name] = !isCollapsed(item)
}

function isGroupActive(item: NavItem) {
  return (item.children || []).some((c) => c.path === route.path)
}

const user = ref<any>(null)
const version = ref('1.0.0')
const alive = ref(true)

const roleLabel = computed(() => {
  const map: Record<string, string> = { admin: '管理员', operator: '运维', viewer: '只读' }
  return map[user.value?.role] || '只读'
})

const offlineHint = computed(() => {
  const h = activeHost.value
  if (!h || h.kind === 'local' || h.online) return ''
  return `主机【${h.name}】当前离线${h.error ? '：' + h.error : ''}，页面数据可能无法加载`
})

function go(p?: string) {
  if (p && route.path !== p) router.push(p)
}

function refresh() {
  alive.value = false
  requestAnimationFrame(() => (alive.value = true))
}

async function reloadHosts() {
  await loadHosts()
  if (activeHost.value?.online) ElMessage.success('主机已恢复连接')
  else ElMessage.warning('主机仍不可达')
}

function onHostChange(id: number) {
  switchHost(id)
  const h = hostList.value.find((x) => x.id === id)
  if (h && h.kind !== 'local' && !h.online) {
    ElMessage.warning(`已切换到【${h.name}】，但该主机当前离线`)
  } else if (h) {
    ElMessage.success(`已切换到【${h.name}】`)
  }
}

async function onCommand(cmd: string) {
  if (cmd === 'settings') return router.push('/settings')
  if (cmd === 'logout') {
    await ElMessageBox.confirm('确定要退出登录吗？', '提示', { type: 'warning' }).catch(() => null)
    try {
      await api('/api/auth/logout', { method: 'POST' })
    } catch {}
    setToken('')
    ElMessage.success('已退出')
    router.push('/login')
  }
}

let hostTimer: any = null

onMounted(async () => {
  try {
    user.value = await api('/api/auth/me')
    const info = await api('/api/system/info')
    version.value = info.version || '1.0.0'
  } catch {}
  await loadHosts()
  // 定期刷新主机状态，让顶栏指示灯保持真实
  hostTimer = setInterval(loadHosts, 30000)
})

onBeforeUnmount(() => {
  if (hostTimer) clearInterval(hostTimer)
})
</script>

<style scoped>
.nav-parent .nav-arrow {
  margin-left: auto;
  font-size: 11px;
  opacity: 0.55;
  transition: transform 0.15s;
}
/* 父级只做文字高亮，避免和子项的高亮块抢视觉重心 */
.nav-parent.active {
  background: transparent;
  box-shadow: none;
  color: #fff;
}
.nav-parent.active:hover {
  background: rgba(255, 255, 255, 0.06);
}
.nav-sub {
  padding-left: 4px;
  margin-bottom: 2px;
}
.nav-sub-item {
  padding: 7px 12px 7px 30px;
  font-size: 12.5px;
  color: #a3aec6;
}
.nav-sub-dot {
  width: 4px;
  height: 4px;
  border-radius: 50%;
  background: #55627c;
  flex: none;
  transition: background 0.15s;
}
.nav-sub-item.active .nav-sub-dot {
  background: #fff;
  box-shadow: 0 0 0 3px rgba(59, 99, 232, 0.35);
}
.host-select {
  width: 186px;
}
.host-select :deep(.el-select__wrapper) {
  box-shadow: 0 0 0 1px #e5e8f0 inset;
  border-radius: 8px;
}
.host-dot {
  display: inline-block;
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: #c3c8d4;
  margin-right: 5px;
  flex: none;
}
.host-dot.on {
  background: #25b26a;
  box-shadow: 0 0 0 3px rgba(37, 178, 106, 0.14);
}
.host-dot.off {
  background: #e05b5b;
  box-shadow: 0 0 0 3px rgba(224, 91, 91, 0.14);
}
.host-opt {
  display: flex;
  align-items: center;
  gap: 2px;
}
.host-name {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.host-tag {
  font-size: 11px;
  color: var(--ink-3);
  margin-left: 8px;
}
.nav-badge {
  margin-left: auto;
  font-size: 10.5px;
  color: var(--ink-3);
  background: #f0f2f7;
  border-radius: 6px;
  padding: 1px 5px;
}
.offline-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  background: #fff6e8;
  border: 1px solid #f6dfb8;
  color: #8a5a12;
  border-radius: 9px;
  padding: 9px 13px;
  font-size: 12.5px;
  margin-bottom: 14px;
}
</style>
