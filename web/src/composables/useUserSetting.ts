// 用户级设置同步：收敛「user_settings 服务端权威 + localStorage 缓存 + 一次性迁移
// 上云 + 防抖回写」模式（AGENTS.md「前端状态边界」对该模式的权威描述）。
//
// 两个使用形态，共享同一内核；各站点的差异语义（parse/serialize、缓存键名、旧键
// 迁移、null 哨兵等）全部经钩子保留在调用侧，不强推统一形态：
//
//  - createUserSetting(key, opts) → 同步通道：值留在调用侧（Pinia options store 的
//    state、组件本地 ref 等），readCache()/writeCache()/sync()/commit() 四段式。
//    内部无任何生命周期依赖，store 与组件 setup 双宿主皆可创建。
//  - useUserSetting(key, opts) → { value, ready, save }：值由 composable 持有的
//    形态，适合组件 setup 里「单一值」的键（缓存先行回显 → 服务端校正 → save 提交）。
//
// 内核语义（全部接入站点统一到最完备形态）：
//  - 共享一次 GET /api/settings：模块级 in-flight 去重，桌面挂载时多个键并发同步
//    只发一个请求（落定后即清空，之后的初始化照常独立拉取）；
//  - 回写门控：服务端同步判定已定（GET 成功）后才回写，防本地初始/空态覆盖云端；
//  - lastSynced 基线去重：内容与最近已知服务端一致时不回写（防访问即改库）；
//  - 防抖回写 PUT /api/settings {key,value}（默认 400ms，可调）；
//  - 回写失败重置基线（下次变更自动重试）；
//  - 服务端无该键时的一次性迁移上云：由调用侧声明（sync 回调自行 commit，或 ref
//    形态的 migrate 选项），默认不迁移。
import { ref, type Ref } from 'vue'
import { api } from '../api/client'

/** 进行中的 /api/settings 请求（模块级 in-flight 去重）。 */
let inflight: Promise<Record<string, string>> | null = null

/** 拉取全量 user_settings：同刻发起的多个键共享一次请求；失败原样抛出，
 *  由调用侧离线容忍。落定（无论成败）后清空，后续调用重新拉取。 */
export function fetchUserSettings(): Promise<Record<string, string>> {
  if (!inflight) {
    const p = api.get<Record<string, string>>('/api/settings')
    inflight = p
    const settle = () => { if (inflight === p) inflight = null }
    p.then(settle, settle)
  }
  return inflight
}

/** 设置键的差异钩子：解析/序列化/缓存键等差异全部留在调用侧。 */
export interface UserSettingOptions<T> {
  /** localStorage 缓存键（函数形态支持按用户分键；缺省则不做本地缓存） */
  cacheKey?: string | (() => string)
  /** 原始字符串 → 值；损坏/无法解析返回 undefined（处置由调用侧决定） */
  parse: (raw: string) => T | undefined
  /** 值 → 存储字符串（localStorage 缓存与服务端回写共用同一序列化） */
  serialize: (v: T) => string
  /** 覆盖缓存读取（默认 getItem(cacheKey) 后 parse）；旧键迁移表达式在此保留 */
  readCache?: () => T | undefined
  /** 回写防抖毫秒数（默认 400） */
  debounce?: number
}

/** 同步通道：值留在调用侧的形态（store 与组件 setup 双宿主通用）。 */
export interface UserSettingChannel<T> {
  /** localStorage 缓存即时读取（同步；用于缓存先行回显）。 */
  readCache(): T | undefined
  /** 只写本地缓存（如服务端数据替换本地后的缓存刷新；不触发服务端回写）。 */
  writeCache(v: T): void
  /** 服务端同步（同一通道幂等，只同步一次）：内部加入共享 GET；GET 成功后回调
   *  onServer(解析值, 服务端是否有该键)，回调返回的值记为回写基线（内容未变
   *  不回写）——只能返回源自服务端数据的内容。返回的 Promise 恒不拒绝（离线
   *  容忍），落定（无论成败）后 resolve，供调用方串联完成信号。 */
  sync(onServer: (value: T | undefined, has: boolean) => T | void): Promise<void>
  /** 提交新值：localStorage 即时写（缓存）+ 服务端防抖回写（权威）。服务端数据
   *  就位前只写缓存（防本地初始态覆盖云端）；回写失败自动重置基线下次重试。 */
  commit(v: T): void
}

/** 创建设置键同步通道。 */
export function createUserSetting<T>(key: string, opts: UserSettingOptions<T>): UserSettingChannel<T> {
  const cacheKey = () => (typeof opts.cacheKey === 'function' ? opts.cacheKey() : opts.cacheKey)
  const debounceMs = opts.debounce ?? 400
  let ready = false    // 服务端数据已就位（就位后的变更才回写服务端）
  let lastSynced = ''  // 最近一次已知服务端内容（无变化不回写，防访问即改库）
  let timer = 0        // 服务端写回防抖
  let synced = false   // sync 幂等

  const readCache =
    opts.readCache ||
    (() => {
      const k = cacheKey()
      if (!k) return undefined
      try {
        const raw = localStorage.getItem(k)
        return raw == null ? undefined : opts.parse(raw)
      } catch { return undefined }
    })

  return {
    readCache,
    writeCache(v) {
      const k = cacheKey()
      if (!k) return
      try { localStorage.setItem(k, opts.serialize(v)) } catch { /* 存储满静默 */ }
    },
    sync(onServer) {
      if (synced) return Promise.resolve()
      synced = true
      return fetchUserSettings().then(
        (s) => {
          ready = true // 同步判定已定，后续变更可回写
          try {
            const raw = s[key]
            const base = onServer(raw === undefined ? undefined : opts.parse(raw), raw !== undefined)
            if (base !== undefined) lastSynced = opts.serialize(base)
          } catch { /* 数据损坏容忍：保持本地，下次变更回写覆盖 */ }
        },
        () => { /* 离线容忍：继续用本地缓存，回写保持关闸 */ },
      )
    },
    commit(v) {
      const raw = opts.serialize(v)
      const k = cacheKey()
      if (k) { try { localStorage.setItem(k, raw) } catch { /* 存储满静默 */ } }
      if (!ready) return
      if (raw === lastSynced) return
      clearTimeout(timer)
      timer = window.setTimeout(() => {
        lastSynced = raw
        api.put('/api/settings', { key, value: raw }).catch(() => { lastSynced = '' })
      }, debounceMs)
    },
  }
}

/** 组件 setup 形态：值由 composable 持有的单一值设置键。 */
export interface UseUserSettingReturn<T> {
  /** 当前值：localStorage 缓存先行回显，服务端数据到达后校正；可直接写入（配合 save 提交）。 */
  value: Ref<T | undefined>
  /** 服务端数据是否已就位（就位后的 save 才会回写服务端）。 */
  ready: Ref<boolean>
  /** 提交当前 value：写缓存 + 服务端防抖回写。 */
  save(): void
}

/** useUserSetting 在通道钩子之上的两个差异语义选项：
 *  absent（服务端无该键时的取值，默认保持缓存值不动，如 dock/启动台的
 *  「无记录 = 默认态」清回语义）与 migrate（无该键时迁移上云的条件，默认不迁移，
 *  如旧版本数据只在 localStorage 的一次性搬运）。 */
export function useUserSetting<T>(
  key: string,
  opts: UserSettingOptions<T> & {
    absent?: (cached: T | undefined) => T | undefined
    migrate?: (v: T | undefined) => boolean
  },
): UseUserSettingReturn<T> {
  const value = ref<T | undefined>()
  const ready = ref(false)
  const ch = createUserSetting(key, opts)
  value.value = ch.readCache()
  void ch.sync((v, has) => {
    ready.value = true
    if (has) {
      if (v !== undefined) {
        value.value = v
        return v // 回写基线 = 服务端值
      }
      return // 数据损坏：保持缓存值（下次 save 回写覆盖）
    }
    const next = opts.absent ? opts.absent(value.value) : value.value
    if (next !== value.value) value.value = next
    if (opts.migrate?.(value.value)) ch.commit(value.value as T)
  })
  return {
    value,
    ready,
    save: () => { if (value.value !== undefined) ch.commit(value.value) },
  }
}
