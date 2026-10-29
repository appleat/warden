import { createRouter, createWebHistory } from 'vue-router'
import { getToken } from './api'

const routes = [
  { path: '/login', component: () => import('./views/Login.vue'), meta: { public: true } },
  {
    path: '/',
    component: () => import('./layouts/MainLayout.vue'),
    redirect: '/dashboard',
    children: [
      {
        path: 'dashboard',
        component: () => import('./views/Dashboard.vue'),
        meta: { title: '数据看板', desc: '主机资源实时概览' }
      },
      {
        path: 'hosts',
        component: () => import('./views/Hosts.vue'),
        meta: { title: '主机管理', desc: '纳管多台服务器，切换管理视角' }
      },
      {
        path: 'containers',
        component: () => import('./views/Containers.vue'),
        meta: { title: '容器管理', desc: 'Docker 容器启停、日志与终端' }
      },
      {
        path: 'nacos',
        component: () => import('./views/Nacos.vue'),
        meta: { title: 'Nacos 服务', desc: '服务注册发现与配置中心管理' }
      },
      {
        path: 'redis',
        component: () => import('./views/Redis.vue'),
        meta: { title: 'Redis 服务', desc: '键浏览、运行指标与命令执行' }
      },
      {
        path: 'files',
        component: () => import('./views/Files.vue'),
        meta: { title: '文件管理', desc: '浏览、上传、下载与在线编辑' }
      },
      {
        path: 'system',
        redirect: '/system/firewall'
      },
      {
        path: 'system/processes',
        component: () => import('./views/Processes.vue'),
        meta: { title: '进程管理', desc: '实时进程列表、资源排序与结束进程' }
      },
      {
        path: 'system/firewall',
        component: () => import('./views/Firewall.vue'),
        meta: { title: '防火墙管理', desc: 'ufw/firewalld 规则与端口' }
      },
      {
        path: 'system/ssh',
        component: () => import('./views/System.vue'),
        meta: { title: 'SSH 主机', desc: '远程主机凭据与连接测试' }
      },
      {
        path: 'system/terminal',
        component: () => import('./views/System.vue'),
        meta: { title: 'SSH 终端管理', desc: '本机 Shell 与远程 SSH 会话' }
      },
      {
        path: 'monitor',
        component: () => import('./views/Monitor.vue'),
        meta: { title: '网站监控', desc: '站点可用性、响应时间与告警' }
      },
      {
        path: 'audit',
        component: () => import('./views/Audit.vue'),
        meta: { title: '审计日志', desc: '所有写操作留痕' }
      },
      {
        path: 'settings',
        component: () => import('./views/Settings.vue'),
        meta: { title: '设置', desc: '账号安全与告警通知' }
      }
    ]
  },
  { path: '/:pathMatch(.*)*', redirect: '/dashboard' }
]

const router = createRouter({
  history: createWebHistory(),
  routes
})

router.beforeEach((to) => {
  const token = getToken()
  if (!to.meta.public && !token) return '/login'
  if (to.path === '/login' && token) return '/dashboard'
  return true
})

export default router
