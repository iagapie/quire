package cosmos

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/iagapie/quire/internal/dialect"
)

// sampleSize is how many items the fields of a container are inferred from.
const sampleSize = 100

// Databases lists the databases of the account.
func (d *Driver) Databases(ctx context.Context, q dialect.Querier) ([]string, error) {
	var out []string
	err := withConn(ctx, q, func(c *conn) (err error) {
		out, err = c.c.databases(ctx)
		return err
	})
	return out, err
}

// Schemas returns nothing: containers are the objects of a database.
func (d *Driver) Schemas(context.Context, dialect.Querier, string) ([]string, error) { return nil, nil }

// Objects lists the containers, with their partition key paths as detail.
func (d *Driver) Objects(ctx context.Context, q dialect.Querier, _, _ string) ([]dialect.Object, error) {
	var out []dialect.Object
	err := withConn(ctx, q, func(c *conn) error {
		props, err := c.c.containerProps(ctx)
		for _, p := range props {
			out = append(out, dialect.Object{Name: p.ID, Kind: dialect.KindContainer, Detail: strings.Join(p.PartitionKeyDefinition.Paths, ", ")})
		}
		return err
	})
	return out, err
}

func (d *Driver) properties(ctx context.Context, q dialect.Querier, container string) (azcosmos.ContainerProperties, error) {
	var props azcosmos.ContainerProperties
	err := withConn(ctx, q, func(c *conn) error {
		db, err := c.c.db(ctx)
		if err != nil {
			return err
		}
		cc, err := db.NewContainer(container)
		if err != nil {
			return err
		}
		resp, err := await(ctx, func() (azcosmos.ContainerResponse, error) { return cc.Read(ctx, nil) })
		if err != nil {
			return friendly(err)
		}
		if resp.ContainerProperties != nil {
			props = *resp.ContainerProperties
		}
		return nil
	})
	return props, err
}

// systemFields are the properties Cosmos DB adds to every item, listed last.
var systemFields = []string{"_rid", "_self", "_etag", "_attachments", "_ts"}

// Columns infers the fields of a container from a sample of its items: top-level
// properties and, one level down, the properties of objects ("address.city"). A field
// missing or null in some items is nullable; mixed types are listed together.
func (d *Driver) Columns(ctx context.Context, q dialect.Querier, _, _, container string) ([]dialect.Column, error) {
	props, err := d.properties(ctx, q, container)
	if err != nil {
		return nil, err
	}
	var items [][]byte
	err = withConn(ctx, q, func(c *conn) error {
		db, err := c.c.db(ctx)
		if err != nil {
			return err
		}
		cc, err := db.NewContainer(container)
		if err != nil {
			return err
		}
		pager := cc.NewQueryItemsPager("SELECT * FROM c", azcosmos.NewPartitionKey(), &azcosmos.QueryOptions{PageSizeHint: sampleSize})
		for pager.More() && len(items) < sampleSize {
			page, err := await(ctx, func() (azcosmos.QueryItemsResponse, error) { return pager.NextPage(ctx) })
			if err != nil {
				return friendly(err)
			}
			items = append(items, page.Items...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	pk := map[string]bool{}
	for _, p := range props.PartitionKeyDefinition.Paths {
		pk[strings.ReplaceAll(strings.TrimPrefix(p, "/"), "/", ".")] = true
	}
	return inferColumns(items, pk), nil
}

type fieldInfo struct {
	types []string
	seen  int
	null  bool
}

func inferColumns(items [][]byte, pk map[string]bool) []dialect.Column {
	var order []string
	fields := map[string]*fieldInfo{}
	note := func(name, typ string) {
		f, ok := fields[name]
		if !ok {
			f = &fieldInfo{}
			fields[name] = f
			order = append(order, name)
		}
		f.seen++
		if typ == "null" {
			f.null = true
			return
		}
		if !slices.Contains(f.types, typ) {
			f.types = append(f.types, typ)
		}
	}
	for _, raw := range items {
		doc, keys, err := decodeOrdered(raw)
		if err != nil {
			continue
		}
		for _, k := range keys {
			v := doc[k]
			note(k, jsonType(v))
			if obj, ok := v.(map[string]any); ok {
				sub := make([]string, 0, len(obj))
				for sk := range obj {
					sub = append(sub, sk)
				}
				sort.Strings(sub)
				for _, sk := range sub {
					note(k+"."+sk, jsonType(obj[sk]))
				}
			}
		}
	}
	// id first, the system properties last.
	rank := func(name string) int {
		switch {
		case name == "id":
			return 0
		case slices.Contains(systemFields, name):
			return 2
		}
		return 1
	}
	slices.SortStableFunc(order, func(a, b string) int { return rank(a) - rank(b) })
	out := make([]dialect.Column, 0, len(order))
	for _, name := range order {
		f := fields[name]
		c := dialect.Column{Name: name, Type: strings.Join(f.types, " | "), Nullable: f.null || f.seen < len(items)}
		if c.Type == "" {
			c.Type = "null"
		}
		if name == "id" {
			c.PK = 1
		}
		if pk[name] {
			c.Comment = "partition key"
		}
		if slices.Contains(systemFields, name) {
			c.Generated, c.Comment = true, "system property"
		}
		out = append(out, c)
	}
	return out
}

// decodeOrdered decodes a JSON object and returns its keys in document order.
func decodeOrdered(raw []byte) (map[string]any, []string, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, nil, err
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	var keys []string
	depth := 0
	expectKey := false
	for {
		tok, err := dec.Token()
		if err != nil {
			return doc, keys, nil //nolint:nilerr // the document decoded above; the keys end with the tokens
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				depth++
				expectKey = depth == 1
			case '[':
				depth++
			case '}', ']':
				depth--
				expectKey = depth == 1
			}
			continue
		case string:
			if depth == 1 && expectKey {
				keys = append(keys, t)
				expectKey = false
				continue
			}
		}
		if depth == 1 {
			expectKey = true
		}
	}
}

func jsonType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "unknown"
}

// Indexes describes the indexing policy: included paths, composite and spatial
// indexes. They are changed in the Azure portal, not here.
func (d *Driver) Indexes(ctx context.Context, q dialect.Querier, _, _, container string) ([]dialect.Index, error) {
	props, err := d.properties(ctx, q, container)
	if err != nil || props.IndexingPolicy == nil {
		return nil, err
	}
	ip := props.IndexingPolicy
	var out []dialect.Index
	for _, p := range ip.IncludedPaths {
		out = append(out, dialect.Index{Name: p.Path, Table: container, Method: "range", Columns: []dialect.IndexColumn{{Name: p.Path}}, SizeBytes: -1})
	}
	for _, p := range ip.ExcludedPaths {
		out = append(out, dialect.Index{Name: p.Path + " (excluded)", Table: container, Method: "excluded", Columns: []dialect.IndexColumn{{Name: p.Path}}, Invisible: true, SizeBytes: -1})
	}
	for i, comp := range ip.CompositeIndexes {
		ix := dialect.Index{Name: fmt.Sprintf("composite %d", i+1), Table: container, Method: "composite", SizeBytes: -1}
		for _, c := range comp {
			ix.Columns = append(ix.Columns, dialect.IndexColumn{Name: c.Path, Desc: strings.EqualFold(string(c.Order), "descending")})
		}
		out = append(out, ix)
	}
	for _, s := range ip.SpatialIndexes {
		out = append(out, dialect.Index{Name: s.Path + " (spatial)", Table: container, Method: "spatial", Columns: []dialect.IndexColumn{{Name: s.Path}}, SizeBytes: -1})
	}
	return out, nil
}

// Constraints lists the partition key and the unique keys.
func (d *Driver) Constraints(ctx context.Context, q dialect.Querier, _, _, container string) ([]dialect.Constraint, error) {
	props, err := d.properties(ctx, q, container)
	if err != nil {
		return nil, err
	}
	out := []dialect.Constraint{{
		Name: "pk", Type: "PARTITION KEY", Columns: props.PartitionKeyDefinition.Paths,
		Definition: "PARTITION KEY (" + strings.Join(props.PartitionKeyDefinition.Paths, ", ") + ")",
	}}
	if props.UniqueKeyPolicy != nil {
		for i, u := range props.UniqueKeyPolicy.UniqueKeys {
			out = append(out, dialect.Constraint{Name: fmt.Sprintf("uk%d", i+1), Type: "UNIQUE", Columns: u.Paths,
				Definition: "UNIQUE (" + strings.Join(u.Paths, ", ") + ")"})
		}
	}
	return out, nil
}

// DDL returns the container properties as JSON.
func (d *Driver) DDL(ctx context.Context, q dialect.Querier, _, _ string, obj dialect.Object) (string, error) {
	props, err := d.properties(ctx, q, obj.Name)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(props)
	if err != nil {
		return "", err
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	for _, k := range []string{"_rid", "_self", "_etag", "_ts", "_docs", "_sprocs", "_triggers", "_udfs", "_conflicts"} {
		delete(v, k)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	return string(out) + "\n", err
}

// TableStats lists the containers; Cosmos DB does not report their sizes here.
func (d *Driver) TableStats(ctx context.Context, q dialect.Querier, database, schema string) ([]dialect.TableStat, error) {
	objs, err := d.Objects(ctx, q, database, schema)
	out := make([]dialect.TableStat, len(objs))
	for i, o := range objs {
		out[i] = dialect.TableStat{Name: o.Name, Kind: o.Kind, Rows: -1, Bytes: -1}
	}
	return out, err
}
