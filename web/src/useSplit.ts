import { computed, nextTick, onBeforeUnmount, onMounted, ref, type Ref } from 'vue'

// 左侧栏宽度（px）。拖动条本身占一列，宽度固定，两侧不再用 gap 分隔。
export const SPLIT_BAR_WIDTH = 12

export interface SplitOptions {
  /** localStorage 里的键名，用于记住用户拖过的宽度；留空则不记忆 */
  storageKey?: string
  /** 默认（也是双击恢复的）宽度 */
  initial?: number
  /** 最小宽度 */
  min?: number
  /** 最大宽度 */
  max?: number
  /** 右侧至少保留的宽度，窗口变窄时按它反推左侧上限 */
  reserve?: number
}

/**
 * 可拖动的左右分栏。
 *
 * 用法：
 *   const { width, style, dragging, container, onPointerDown, reset } = useSplitWidth({...})
 *
 *   <div class="split" :style="style" ref="container">
 *     <div class="card side">...</div>
 *     <div class="split-bar" :class="{ 'is-dragging': dragging }"
 *          @pointerdown="onPointerDown" @dblclick="reset"
 *          title="拖动调整宽度，双击恢复默认"></div>
 *     <div class="card">...</div>
 *   </div>
 *
 * `.split` 自身要写 `display: grid; gap: 0`，列宽由这里返回的 style 决定。
 */
export function useSplitWidth(opts: SplitOptions = {}) {
  const { storageKey = '', initial = 320, min = 200, max = 640, reserve = 280 } = opts

  const width = ref(initial)
  const dragging = ref(false)
  const container: Ref<HTMLElement | null> = ref(null)

  // 容器没量出宽度时（例如页面在隐藏的页签里）用视口粗估，
  // 只靠 max 兜底的话，窗口缩小时左栏会一直占着不放、把右栏挤没。
  function upper(): number {
    let w = container.value?.clientWidth || 0
    if (w <= 0) w = window.innerWidth - 220 // 侧栏 + 页面留白的大致宽度
    return Math.max(min, Math.min(max, w - reserve))
  }

  function clamp(v: number): number {
    return Math.min(upper(), Math.max(min, Math.round(v)))
  }

  function set(v: number) {
    width.value = clamp(v)
  }

  function persist() {
    if (!storageKey) return
    try {
      localStorage.setItem(storageKey, String(width.value))
    } catch {
      // 隐私模式下 localStorage 可能不可写，忽略即可
    }
  }

  function restore() {
    if (!storageKey) return
    let saved = 0
    try {
      saved = Number(localStorage.getItem(storageKey)) || 0
    } catch {
      saved = 0
    }
    if (saved > 0) width.value = clamp(saved)
  }

  function reset() {
    width.value = clamp(initial)
    persist()
  }

  function onPointerDown(e: PointerEvent) {
    // 只响应左键（触摸/触控笔没有 button 概念，pointerType 不是 mouse 时放行）
    if (e.pointerType === 'mouse' && e.button !== 0) return
    const el = e.currentTarget as HTMLElement | null
    if (!el) return
    e.preventDefault()

    const startX = e.clientX
    const startW = width.value
    dragging.value = true
    // 拖动时鼠标会离开细条，禁掉文本选中，避免糊一片蓝
    document.body.style.userSelect = 'none'

    const move = (ev: PointerEvent) => set(startW + ev.clientX - startX)
    const finish = () => {
      dragging.value = false
      document.body.style.userSelect = ''
      el.removeEventListener('pointermove', move)
      el.removeEventListener('pointerup', finish)
      el.removeEventListener('pointercancel', finish)
      try {
        el.releasePointerCapture(e.pointerId)
      } catch {
        // 指针已经释放时会抛错，忽略
      }
      persist()
    }

    // 指针捕获后，即使光标移出 12px 的窄条也能继续收到事件
    try {
      el.setPointerCapture(e.pointerId)
    } catch {
      // 不支持捕获的环境退化为普通事件，仍能工作（只是移出条子会断）
    }
    el.addEventListener('pointermove', move)
    el.addEventListener('pointerup', finish)
    el.addEventListener('pointercancel', finish)
  }

  const style = computed(() => ({
    gridTemplateColumns: `${width.value}px ${SPLIT_BAR_WIDTH}px minmax(0, 1fr)`
  }))

  let ro: ResizeObserver | null = null

  // 窗口变窄后，记下的宽度可能超过「容器宽 - 右侧保留宽」，收回一点保证右侧可用。
  // 拖动过程中不动，否则手感会跳。
  function reflow() {
    if (dragging.value) return
    const fixed = clamp(width.value)
    if (fixed !== width.value) width.value = fixed
  }

  onMounted(() => {
    restore()
    reflow()
    // 视口变化用 resize 兜底：ResizeObserver 在某些容器（如隐藏页签里的节点）拿不到尺寸，
    // 两条路子都挂上更稳，重复触发也只是多算一次 clamp。
    window.addEventListener('resize', reflow)
    if (typeof ResizeObserver !== 'undefined') {
      ro = new ResizeObserver(reflow)
      // 等一帧再观察：模板 ref 与 el-tab-pane 的渲染顺序不保证在 onMounted 时已就绪
      nextTick(() => {
        if (ro && container.value) ro.observe(container.value)
        reflow()
      })
    }
  })
  onBeforeUnmount(() => {
    window.removeEventListener('resize', reflow)
    ro?.disconnect()
    ro = null
    document.body.style.userSelect = ''
  })

  return { width, style, dragging, container, onPointerDown, reset, set }
}
