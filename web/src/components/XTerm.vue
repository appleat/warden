<template>
  <div class="term-wrap">
    <div class="term-bar">
      <span class="dot red"></span><span class="dot yellow"></span><span class="dot green"></span>
      <span style="margin-left: 8px">{{ title }}</span>
      <span v-if="statusText" class="muted" style="color: #6b7691">· {{ statusText }}</span>
      <div class="spacer"></div>
      <el-button link size="small" style="color: #93a0bb" @click="reconnect">重连</el-button>
    </div>
    <div ref="host" class="term-host"></div>
    <div v-if="notice" class="term-notice">
      <div class="notice-box">
        <div class="notice-title">会话未能建立</div>
        <p>{{ notice }}</p>
        <el-button size="small" type="primary" plain @click="reconnect">重试</el-button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'

const props = defineProps<{ url: string; title?: string }>()
const host = ref<HTMLDivElement>()
const statusText = ref('连接中')
const notice = ref('')
const NOTICE = '\u0000NOTICE:'

let term: Terminal | null = null
let fit: FitAddon | null = null
let ws: WebSocket | null = null
let disposed = false

function fitSize() {
  if (!fit || !term) return
  try {
    fit.fit()
  } catch {}
}

function sendResize() {
  if (ws && ws.readyState === WebSocket.OPEN && term) {
    ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }))
  }
}

function connect() {
  if (disposed || !props.url) return
  statusText.value = '连接中'
  ws = new WebSocket(props.url)
  ws.binaryType = 'arraybuffer'

  ws.onopen = () => {
    notice.value = ''
    statusText.value = '已连接'
    nextTick(() => {
      fitSize()
      sendResize()
    })
  }
  ws.onmessage = (ev) => {
    if (!term) return
    if (typeof ev.data === 'string') {
      if (ev.data.startsWith(NOTICE)) {
        notice.value = ev.data.slice(NOTICE.length)
        statusText.value = ''
        return
      }
      term.write(ev.data)
    } else {
      term.write(new Uint8Array(ev.data as ArrayBuffer))
    }
  }
  ws.onclose = () => {
    statusText.value = '已断开'
  }
  ws.onerror = () => {
    statusText.value = '连接失败'
  }
}

function reconnect() {
  if (ws) {
    ws.onclose = null
    ws.close()
  }
  term?.reset()
  connect()
}

function onWinResize() {
  fitSize()
  sendResize()
}

// 容器宽度变化（例如右侧文件面板滑入/拖拽宽度）时也要重新 fit，
// 否则终端列数不变会溢出或留白。动画期间会连续触发，做 120ms 防抖，避免频繁给 PTY 发 resize。
let ro: ResizeObserver | null = null
let roTimer: any = null
function observeSize() {
  if (!host.value || typeof ResizeObserver === 'undefined') return
  ro = new ResizeObserver(() => {
    clearTimeout(roTimer)
    roTimer = setTimeout(() => {
      if (!host.value || host.value.offsetWidth < 40 || host.value.offsetHeight < 20) return
      fitSize()
      sendResize()
    }, 120)
  })
  ro.observe(host.value)
}

onMounted(() => {
  if (!host.value) return
  term = new Terminal({
    fontSize: 13,
    fontFamily: "'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, monospace",
    theme: {
      background: '#101725',
      foreground: '#d5dcea',
      cursor: '#7aa2ff',
      selectionBackground: '#2a3a5c'
    },
    cursorBlink: true,
    scrollback: 4000,
    allowProposedApi: true
  })
  fit = new FitAddon()
  term.loadAddon(fit)
  term.open(host.value)
  fitSize()

  term.onData((d) => {
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(d)
  })
  term.onResize(() => sendResize())

  window.addEventListener('resize', onWinResize)
  observeSize()
  connect()
})

onBeforeUnmount(() => {
  disposed = true
  clearTimeout(roTimer)
  if (ro) {
    ro.disconnect()
    ro = null
  }
  window.removeEventListener('resize', onWinResize)
  if (ws) {
    ws.onclose = null
    ws.close()
  }
  term?.dispose()
  term = null
})

watch(
  () => props.url,
  () => reconnect()
)

// 父组件在侧栏动画结束后可以主动要求重排
defineExpose({
  refit: () => {
    fitSize()
    sendResize()
  }
})
</script>

<style scoped>
.term-wrap {
  display: flex;
  flex-direction: column;
  position: relative;
}
.term-notice {
  position: absolute;
  inset: 42px 8px 8px;
  display: grid;
  place-items: center;
  background: rgba(16, 23, 37, 0.92);
  border-radius: 0 0 8px 8px;
}
.notice-box {
  text-align: center;
  max-width: 420px;
  padding: 0 20px;
}
.notice-title {
  color: #f0f3fa;
  font-size: 14px;
  font-weight: 500;
  margin-bottom: 8px;
}
.notice-box p {
  color: #9aa6bf;
  font-size: 12.5px;
  line-height: 1.7;
  margin: 0 0 14px;
}
.term-host {
  flex: 1;
  padding: 6px 4px 4px 8px;
  min-height: 0;
}
</style>
