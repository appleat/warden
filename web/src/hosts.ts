import { computed, ref } from 'vue'
import { api, getHostId, setHostId } from './api'

export interface HostItem {
  id: number
  name: string
  kind: 'local' | 'agent'
  address: string
  tls: boolean
  insecure: boolean
  group_name: string
  remark: string
  enabled: boolean
  // 运行时状态
  online: boolean
  latency_ms: number
  version: string
  hostname: string
  platform: string
  kernel: string
  arch: string
  cpu_cores: number
  total_mem: number
  uptime: number
  docker_ok: boolean
  firewall: string
  file_root: string
  error: string
  checked_at?: string
}

export const hostList = ref<HostItem[]>([])
export const activeHostId = ref<number>(getHostId())
export const hostsLoading = ref(false)

export const activeHost = computed<HostItem | null>(
  () => hostList.value.find((h) => h.id === activeHostId.value) || null
)

export const remoteHosts = computed(() => hostList.value.filter((h) => h.kind !== 'local'))
export const onlineCount = computed(() => hostList.value.filter((h) => h.online).length)

export async function loadHosts(): Promise<HostItem[]> {
  hostsLoading.value = true
  try {
    const res = await api<{ items: HostItem[] }>('/api/hosts')
    hostList.value = res?.items || []
  } catch {
    hostList.value = []
  } finally {
    hostsLoading.value = false
  }
  // 当前主机被删除时回退到本机
  if (!hostList.value.some((h) => h.id === activeHostId.value)) {
    const local = hostList.value.find((h) => h.kind === 'local')
    if (local) switchHost(local.id)
  }
  return hostList.value
}

export function switchHost(id: number) {
  setHostId(id)
  activeHostId.value = id
}
