<template>
  <div ref="host" :style="{ height: height, width: '100%' }"></div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
// 按需引入：只注册实际用到的图表与组件，避免把整个 echarts（约 1 MB）打进包里。
// 新增图表类型时，记得在下面 use() 里补上对应的 Chart / Component，否则图表会静默不渲染。
import * as echarts from 'echarts/core'
import { LineChart, ScatterChart } from 'echarts/charts'
import { GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'

echarts.use([
  LineChart,
  ScatterChart,
  GridComponent,
  LegendComponent,
  TooltipComponent,
  CanvasRenderer
])

const props = defineProps<{ option: any; height?: string }>()
const host = ref<HTMLDivElement>()
let chart: ReturnType<typeof echarts.init> | null = null
let ro: ResizeObserver | null = null

onMounted(() => {
  if (!host.value) return
  chart = echarts.init(host.value)
  chart.setOption(props.option)
  ro = new ResizeObserver(() => chart && chart.resize())
  ro.observe(host.value)
})

watch(
  () => props.option,
  (o) => chart && chart.setOption(o, true),
  { deep: true }
)

onBeforeUnmount(() => {
  ro && ro.disconnect()
  chart && chart.dispose()
  chart = null
})
</script>
