package mysql

import (
	"errors"
	"strconv"
	"strings"

	"github.com/iagapie/quire/internal/dialect"
)

// CreateIndex returns the statement creating ix: CREATE INDEX, or ALTER TABLE … ADD
// PRIMARY KEY for the primary key.
func (d *Driver) CreateIndex(database, _ string, ix dialect.Index, _, ifNotExists bool) (string, error) {
	if len(ix.Columns) == 0 {
		return "", errors.New("an index needs at least one column")
	}
	parts := make([]string, 0, len(ix.Columns))
	for _, c := range ix.Columns {
		var p string
		switch {
		case strings.TrimSpace(c.Expression) != "":
			p = "(" + strings.TrimSpace(c.Expression) + ")"
		case c.Name != "":
			p = d.Quote(c.Name)
			if c.Length > 0 {
				p += "(" + strconv.Itoa(c.Length) + ")"
			}
		default:
			return "", errors.New("every index key needs a column or an expression")
		}
		if c.Desc {
			p += " DESC"
		}
		parts = append(parts, p)
	}
	table := d.Qualify(database, "", ix.Table)
	if ix.Primary {
		return "ALTER TABLE " + table + " ADD PRIMARY KEY (" + strings.Join(parts, ", ") + ")", nil
	}
	var b strings.Builder
	b.WriteString("CREATE ")
	switch {
	case ix.Method == "fulltext":
		b.WriteString("FULLTEXT ")
	case ix.Method == "spatial":
		b.WriteString("SPATIAL ")
	case ix.Unique:
		b.WriteString("UNIQUE ")
	}
	b.WriteString("INDEX ")
	if ifNotExists && d.maria {
		b.WriteString("IF NOT EXISTS ")
	}
	if ix.Name == "" {
		return "", errors.New("MySQL indexes need a name")
	}
	b.WriteString(d.Quote(ix.Name))
	if ix.Method == "hash" {
		b.WriteString(" USING HASH")
	}
	b.WriteString(" ON " + table + " (" + strings.Join(parts, ", ") + ")")
	if ix.Invisible && !d.maria {
		b.WriteString(" INVISIBLE")
	}
	return b.String(), nil
}

// DropIndex returns the statement dropping an index.
func (d *Driver) DropIndex(database, _, table, name string, _ bool) string {
	if name == "PRIMARY" {
		return "ALTER TABLE " + d.Qualify(database, "", table) + " DROP PRIMARY KEY"
	}
	return "DROP INDEX " + d.Quote(name) + " ON " + d.Qualify(database, "", table)
}
