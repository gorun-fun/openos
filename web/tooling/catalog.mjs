#!/usr/bin/env node
// tooling/catalog.mjs — 货架 catalog.json 生成/合并 CLI（App 独立发布通道）。
//
// 用法（App 构建脚本在产物落位后调用）：
//   node catalog.mjs --out <货架目录> --id rooms --version 1.0.3 \
//        --entry files/rooms/1.0.3/rooms.js [--min-host v0.0.21] [--hash]
//
//   --out       货架目录（catalog.json 所在，如 public/market 或 market-dist）
//   --id        appId（与宿主 createOpenOS({apps}) 注册键一致）
//   --version   本次发布的 App 版本（自由字符串，建议点分递增）
//   --entry     产物相对货架根路径（约定 files/<id>/<version>/<id>.js）
//   --min-host  最低宿主版本：低于此版本的宿主不用该条目（回落本地构建，见 remote.ts）
//   --hash      可选：计算产物文件 sha256 记入条目（审计用；相对 --out 解析）
//
// 合并语义：只更新 apps[id] 条目（其余条目与 files/ 历史版本目录不动）——
// 回滚 = 再跑一次 --entry 指旧版本路径；updatedAt 记发布时间。
import { createHash } from 'node:crypto'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'

const args = process.argv.slice(2)
const opt = (name) => {
  const i = args.indexOf(`--${name}`)
  return i >= 0 ? args[i + 1] : undefined
}

const out = opt('out'), id = opt('id'), version = opt('version'), entry = opt('entry')
if (!out || !id || !version || !entry) {
  console.error('用法: catalog.mjs --out <货架目录> --id <appId> --version <v> --entry <相对路径> [--min-host <v>] [--hash]')
  process.exit(1)
}

const catalogPath = resolve(out, 'catalog.json')
let catalog = { version: 1, updatedAt: new Date().toISOString(), apps: {} }
if (existsSync(catalogPath)) {
  try {
    catalog = JSON.parse(readFileSync(catalogPath, 'utf8'))
  } catch (e) {
    console.error(`catalog.mjs: 既有 catalog.json 解析失败: ${e.message}`)
    process.exit(1)
  }
}
if (typeof catalog.apps !== 'object' || !catalog.apps) catalog.apps = {}

const item = { version, entry }
const minHost = opt('min-host')
if (minHost) item.minHostVersion = minHost
if (opt('hash') !== undefined) {
  const file = resolve(out, entry)
  item.hash = 'sha256:' + createHash('sha256').update(readFileSync(file)).digest('hex')
}
catalog.apps[id] = item
catalog.updatedAt = new Date().toISOString()

writeFileSync(catalogPath, JSON.stringify(catalog, null, 2) + '\n')
console.log(`catalog.mjs: ${id}@${version} 已发布 → ${entry}${minHost ? `（minHost ${minHost}）` : ''}`)
