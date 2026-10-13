import { describe, expect, it } from 'vitest'
import { DIALECT_SPLIT, split, statementAt } from '../split'

const texts = (s: string, o = {}) => split(s, o).map((x) => x.text)

describe('split', () => {
  it('follows the ClickHouse rules: # comments, backslash escapes, heredocs', () => {
    const o = DIALECT_SPLIT.ClickHouse
    expect(texts("SELECT 'a\\';b'; # c;\nSELECT $h$x;y$h$", o)).toEqual(["SELECT 'a\\';b'", 'SELECT $h$x;y$h$'])
  })
  it('splits on semicolons outside strings, identifiers and comments', () => {
    expect(texts("select ';'; select \"a;b\"; select 1 -- c;\n; select /* ; */ 2")).toEqual(["select ';'", 'select "a;b"', 'select 1 -- c;', 'select /* ; */ 2'])
  })

  it('drops empty statements and leading comments', () => {
    expect(texts(';;\n-- intro\n/* doc */ select 1;;')).toEqual(['select 1'])
  })

  it('handles PostgreSQL dollar quotes and parameters', () => {
    const o = DIALECT_SPLIT.PostgreSQL
    expect(texts('create function f() as $$ select 1; $$ language sql; select $1', o)).toEqual(['create function f() as $$ select 1; $$ language sql', 'select $1'])
    expect(texts('do $b$ begin perform 1; end $b$; select 2', o)).toEqual(['do $b$ begin perform 1; end $b$', 'select 2'])
  })

  it('honours the MySQL DELIMITER command and BEGIN…END blocks', () => {
    const o = DIALECT_SPLIT.MySQL
    expect(texts('DELIMITER $$\nCREATE PROCEDURE p() BEGIN SELECT 1; END$$\nDELIMITER ;\nSELECT 3;', o)).toEqual(['CREATE PROCEDURE p() BEGIN SELECT 1; END', 'SELECT 3'])
    expect(texts('CREATE PROCEDURE p() BEGIN IF 1 THEN SELECT 1; END IF; END; SELECT 3', o)).toEqual(['CREATE PROCEDURE p() BEGIN IF 1 THEN SELECT 1; END IF; END', 'SELECT 3'])
  })

  it('splits SQL Server batches on GO', () => {
    expect(texts('DECLARE @x int; SET @x = 1;\nSELECT @x\nGO\nSELECT 2', DIALECT_SPLIT.MSSQL)).toEqual(['DECLARE @x int; SET @x = 1;\nSELECT @x', 'SELECT 2'])
  })

  it('ends Oracle PL/SQL blocks with a slash', () => {
    expect(texts('BEGIN\n  NULL;\nEND;\n/\nselect 1 from dual;', DIALECT_SPLIT.PLSQL)).toEqual(['BEGIN\n  NULL;\nEND;', 'select 1 from dual'])
  })

  it('keeps SQLite trigger bodies together', () => {
    expect(texts('CREATE TRIGGER t AFTER INSERT ON a BEGIN UPDATE b SET x = CASE WHEN 1 THEN 2 END; END; SELECT 1', DIALECT_SPLIT.SQLite)).toEqual([
      'CREATE TRIGGER t AFTER INSERT ON a BEGIN UPDATE b SET x = CASE WHEN 1 THEN 2 END; END',
      'SELECT 1',
    ])
  })
})

describe('statementAt', () => {
  const script = 'select 1; select 2;\n\nselect 3'
  const stmts = split(script)
  it('finds the statement under or before the cursor', () => {
    expect(statementAt(script, stmts, 0)?.text).toBe('select 1')
    expect(statementAt(script, stmts, 9)?.text).toBe('select 1')
    expect(statementAt(script, stmts, 12)?.text).toBe('select 2')
    expect(statementAt(script, stmts, 20)?.text).toBe('select 2')
    expect(statementAt(script, stmts, script.length)?.text).toBe('select 3')
    expect(statementAt('', [], 0)).toBeNull()
  })
})
