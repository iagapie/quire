import { describe, expect, it } from 'vitest'
import type { TransferItem, TransferJob } from '../api'
import { clock, compact, eta, exportFileName, isFinished, itemFraction, jobFraction, jobTitle, prettyPath, rate, totals } from '../transfer'

const item = (p: Partial<TransferItem>): TransferItem =>
  ({ table: 'c', file: 'c.jsonl', state: 'running', rows: 0, total: -1, bytes: 0, totalBytes: 0, failed: 0, error: '', warning: '', info: '', retry: '', elapsedMs: 0, ...p }) as TransferItem

const job = (kind: string, items: TransferItem[], state = 'running'): TransferJob =>
  ({ id: 'j', kind, sessionId: 's', database: 'db', schema: '', dir: '/tmp', format: 'jsonl', mode: 'insert', state, startedAt: '', elapsedMs: 0, items }) as TransferJob

describe('itemFraction', () => {
  it('uses rows, then bytes, then state', () => {
    expect(itemFraction(item({ rows: 25, total: 100 }))).toBe(0.25)
    expect(itemFraction(item({ bytes: 50, totalBytes: 200 }))).toBe(0.25)
    expect(itemFraction(item({ rows: 200, total: 100 }))).toBe(1)
    expect(itemFraction(item({}))).toBeNull()
    expect(itemFraction(item({ total: 0 }))).toBeNull()
    expect(itemFraction(item({ state: 'done' }))).toBe(1)
    expect(itemFraction(item({ state: 'pending', total: 10 }))).toBe(0)
    expect(itemFraction(item({ state: 'failed', total: 0 }))).toBe(1)
  })
})

describe('jobFraction', () => {
  it('weights exports by rows and imports by bytes', () => {
    const e = job('export', [item({ state: 'done', total: 900, rows: 900 }), item({ state: 'pending', total: 100 })])
    expect(jobFraction(e)).toBeCloseTo(0.9)
    const i = job('import', [item({ bytes: 50, totalBytes: 100 }), item({ state: 'pending', totalBytes: 300 })])
    expect(jobFraction(i)).toBeCloseTo(0.125)
    expect(jobFraction(job('export', []))).toBe(0)
  })
})

describe('totals', () => {
  it('sums items and marks unknown totals', () => {
    const t = totals(
      job('import', [
        item({ state: 'done', rows: 10, total: 10, bytes: 5 }),
        item({ state: 'failed', rows: 3, failed: 1 }),
        item({ state: 'cancelled' }),
      ]),
    )
    expect(t).toMatchObject({ rows: 13, total: -1, bytes: 5, failed: 1, done: 1, failedItems: 1, cancelledItems: 1 })
    expect(totals(job('export', [item({ total: 4 }), item({ total: 6 })])).total).toBe(10)
  })
})

describe('formatting', () => {
  it('formats clocks, rates and estimates', () => {
    expect(clock(0)).toBe('0:00')
    expect(clock(42_000)).toBe('0:42')
    expect(clock(725_000)).toBe('12:05')
    expect(clock(3_729_000)).toBe('1:02:09')
    expect(rate(1000, 2000)).toBe(500)
    expect(rate(1000, 100)).toBe(0)
    expect(eta(0.5, 10_000)).toBe(10_000)
    expect(eta(0.01, 10_000)).toBeNull()
    expect(eta(1, 10_000)).toBeNull()
    expect(compact(1234)).toBe((1234).toLocaleString())
    expect(compact(1_500_000)).toMatch(/1\.5/)
    expect(prettyPath('/Users/igor/Desktop/dump')).toBe('~/Desktop/dump')
    expect(prettyPath('/srv/dump')).toBe('/srv/dump')
  })

  it('mirrors the backend file names', () => {
    expect(exportFileName('orders', 'jsonl', false)).toBe('orders.jsonl')
    expect(exportFileName('a/b:c', 'csv', true)).toBe('a_b_c.csv.gz')
    expect(exportFileName('..', 'json', false)).toBe('_...json')
  })

  it('titles jobs by state', () => {
    expect(jobTitle(job('export', [item({}), item({})]))).toBe('Exporting 2 tables')
    expect(jobTitle(job('import', [item({})], 'done'))).toBe('Import finished')
    expect(jobTitle(job('import', [item({})], 'failed'))).toBe('Import finished with errors')
    expect(jobTitle(job('export', [item({})], 'cancelled'))).toBe('Export cancelled')
    expect(isFinished('done') && isFinished('failed') && isFinished('cancelled') && !isFinished('running')).toBe(true)
  })
})
