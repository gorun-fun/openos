// usePoll.ts — 间隔轮询组合式（自 grf-os 宿主 composables.ts 上游化移植，
// 语义不变）。错误静默（由调用方 toast）。
import { onBeforeUnmount, onMounted, watch, type Ref } from 'vue'

export function usePoll(fn: () => Promise<unknown> | unknown, ms: number, immediate = true, enabled?: Ref<boolean>) {
  let timer = 0
  let stopped = false
  const run = () => { if (!stopped && (!enabled || enabled.value)) Promise.resolve(fn()).catch(() => {}) }
  const start = () => { if (!timer && ms > 0) timer = window.setInterval(run, ms) }
  const stop = () => { if (timer) { clearInterval(timer); timer = 0 } }
  onMounted(() => {
    if (immediate) run()
    if (!enabled || enabled.value) start()
  })
  if (enabled) {
    watch(enabled, (on) => {
      if (stopped) return
      if (on) { run(); start() } else stop()
    })
  }
  onBeforeUnmount(() => { stopped = true; stop() })
  return run
}
