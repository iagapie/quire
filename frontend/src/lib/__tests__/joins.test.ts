import { describe, expect, it } from 'vitest'
import { aliasFor, baseName, joinSuggestions, tablesInStatement, type ForeignKeyRef } from '../joins'

const joins: ForeignKeyRef[] = [
  { table: 'orders', columns: ['customer_id'], refTable: 'customers', refColumns: ['id'] },
  { table: 'order_items', columns: ['order_id'], refTable: 'orders', refColumns: ['id'] },
  { table: 'order_items', columns: ['tenant', 'sku'], refTable: 'products', refColumns: ['tenant', 'sku'] },
]

describe('tablesInStatement', () => {
  it('finds tables and aliases', () => {
    expect(tablesInStatement('SELECT * FROM sales.orders o JOIN "Customers" AS c ON c.id = o.cid LEFT JOIN [dbo].[items] WHERE 1')).toEqual([
      { name: 'orders', alias: 'o' },
      { name: 'Customers', alias: 'c' },
      { name: 'items', alias: 'items' },
    ])
    expect(baseName('`shop`.`order items`')).toBe('order items')
  })
})

describe('aliasFor', () => {
  it('uses initials and stays unique', () => {
    expect(aliasFor('order_items', [])).toBe('oi')
    expect(aliasFor('orders', ['o'])).toBe('o2')
    expect(aliasFor('orderItems', [])).toBe('oi')
    expect(aliasFor('on_hold', [])).toBe('oh')
    expect(aliasFor('order', [])).toBe('o')
  })
})

describe('joinSuggestions', () => {
  it('offers related tables after JOIN', () => {
    const s = joinSuggestions('SELECT * FROM orders o JOIN ', joins)
    expect(s.map((x) => x.apply)).toEqual(['customers c ON c.id = o.customer_id', 'order_items oi ON o.id = oi.order_id'])
  })
  it('offers conditions after ON, both directions and composite keys', () => {
    expect(joinSuggestions('SELECT * FROM customers c JOIN orders AS x ON ', joins).map((x) => x.apply)).toEqual(['c.id = x.customer_id'])
    expect(joinSuggestions('SELECT * FROM products p JOIN order_items oi ON ', joins).map((x) => x.apply)).toEqual(['p.tenant = oi.tenant AND p.sku = oi.sku'])
  })
  it('quotes names when asked', () => {
    const q = (s: string) => `"${s}"`
    expect(joinSuggestions('SELECT * FROM orders o JOIN ', joins, q)[0].apply).toBe('"customers" c ON "c"."id" = "o"."customer_id"')
  })
  it('stays out of the way elsewhere', () => {
    expect(joinSuggestions('SELECT * FROM orders o WHERE ', joins)).toEqual([])
    expect(joinSuggestions('SELECT * FROM orders o JOIN ', [])).toEqual([])
  })
})
