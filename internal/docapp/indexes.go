package docapp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/iagapie/quire/internal/docsession"
	"github.com/iagapie/quire/internal/shell"
)

// IndexDetails is an index definition plus usage statistics, for the index editor.
type IndexDetails struct {
	// Spec is the listIndexes document as canonical Extended JSON.
	Spec string `json:"spec"`
	// SizeBytes is the index size from collStats, or -1 when unavailable.
	SizeBytes int64 `json:"sizeBytes"`
	// Ops is the number of operations that used the index ($indexStats), or -1.
	Ops int64 `json:"ops"`
	// Since is when Ops started counting (RFC 3339), if known.
	Since string `json:"since"`
}

// IndexSaveResult describes what saving an index did.
type IndexSaveResult struct {
	Name string `json:"name"`
	// Action is "created", "modified" (in place, via collMod) or "recreated" (dropped and created again).
	Action string `json:"action"`
}

// Fields that collMod can change without rebuilding the index.
var collModFields = []string{"expireAfterSeconds", "hidden"}

// Fields reported by listIndexes that are not creation options.
var serverOnlyFields = []string{"v", "ns", "textIndexVersion"}

// GetIndex returns the definition and statistics of an index.
func (sh *Shell) GetIndex(ctx context.Context, sessionID, db, coll, name string) (IndexDetails, error) {
	s, err := sh.sessions.Get(sessionID)
	if err != nil {
		return IndexDetails{}, err
	}
	ctx, done := s.Begin(ctx, "")
	defer done()
	spec, err := docsession.RetryRead(ctx, nil, func(ctx context.Context) (bson.D, error) {
		return findIndex(ctx, s, db, coll, name)
	})
	if err != nil {
		return IndexDetails{}, err
	}
	raw, err := bson.MarshalExtJSON(textKeyAsFields(spec), true, false)
	if err != nil {
		return IndexDetails{}, err
	}
	d := IndexDetails{Spec: string(raw), SizeBytes: -1, Ops: -1}

	// Statistics are best effort: they need privileges or features DocumentDB may lack.
	var stats struct {
		IndexSizes map[string]int64 `bson:"indexSizes"`
	}
	if s.Client.Database(db).RunCommand(ctx, bson.D{{Key: "collStats", Value: coll}}).Decode(&stats) == nil {
		if size, ok := stats.IndexSizes[name]; ok {
			d.SizeBytes = size
		}
	}
	cur, err := s.Client.Database(db).Collection(coll).Aggregate(ctx, bson.A{bson.D{{Key: "$indexStats", Value: bson.D{}}}})
	if err == nil {
		var rows []struct {
			Name     string `bson:"name"`
			Accesses struct {
				Ops   int64         `bson:"ops"`
				Since bson.DateTime `bson:"since"`
			} `bson:"accesses"`
		}
		if cur.All(ctx, &rows) == nil {
			for _, r := range rows {
				if r.Name == name {
					d.Ops = r.Accesses.Ops
					d.Since = r.Accesses.Since.Time().UTC().Format("2006-01-02T15:04:05Z")
				}
			}
		}
	}
	return d, nil
}

func findIndex(ctx context.Context, s *docsession.Session, db, coll, name string) (bson.D, error) {
	cur, err := s.Client.Database(db).Collection(coll).Indexes().List(ctx)
	if err != nil {
		return nil, err
	}
	var specs []bson.D
	if err := cur.All(ctx, &specs); err != nil {
		return nil, err
	}
	for _, sp := range specs {
		if n, _ := find(sp, "name"); n == name {
			return sp, nil
		}
	}
	return nil, fmt.Errorf("index %q not found on %s.%s", name, db, coll)
}

// SaveIndex creates an index (original == "") or changes an existing one. The spec is
// a shell-syntax document as accepted by createIndexes: { key: {...}, name: "...", ... }.
// TTL and hidden changes are applied in place; anything else drops and recreates the
// index, restoring the previous definition if the new one cannot be built.
func (sh *Shell) SaveIndex(ctx context.Context, sessionID, db, coll, original, specText string) (IndexSaveResult, error) {
	spec, err := parseIndexSpec(specText)
	if err != nil {
		return IndexSaveResult{}, err
	}
	s, err := sh.sessions.Get(sessionID)
	if err != nil {
		return IndexSaveResult{}, err
	}
	if s.Profile.ReadOnly {
		return IndexSaveResult{}, errReadOnly
	}
	ctx, done := s.Begin(ctx, "")
	defer done()
	name, _ := find(spec, "name")
	res := IndexSaveResult{Name: name.(string)}

	if original == "" {
		res.Action = "created"
		return res, createIndex(ctx, s, db, coll, spec)
	}
	if original == "_id_" {
		return res, errors.New("the _id index cannot be changed")
	}
	current, err := findIndex(ctx, s, db, coll, original)
	if err != nil {
		return res, err
	}
	if changed, inPlace := diffIndex(current, spec); len(changed) == 0 {
		res.Action = "unchanged"
		return res, nil
	} else if inPlace && original == res.Name {
		mod := bson.D{{Key: "name", Value: original}}
		for _, f := range changed {
			v, ok := find(spec, f)
			if !ok {
				// Removing the TTL or un-hiding is expressed with explicit values.
				switch f {
				case "hidden":
					v = false
				case "expireAfterSeconds":
					return res, recreate(ctx, s, db, coll, original, current, spec, &res)
				}
			}
			mod = append(mod, bson.E{Key: f, Value: v})
		}
		res.Action = "modified"
		return res, s.Client.Database(db).RunCommand(ctx, bson.D{{Key: "collMod", Value: coll}, {Key: "index", Value: mod}}).Err()
	}
	return res, recreate(ctx, s, db, coll, original, current, spec, &res)
}

func recreate(ctx context.Context, s *docsession.Session, db, coll, original string, current, spec bson.D, res *IndexSaveResult) error {
	res.Action = "recreated"
	indexes := s.Client.Database(db).Collection(coll).Indexes()
	if err := indexes.DropOne(ctx, original); err != nil {
		return fmt.Errorf("drop %s: %w", original, err)
	}
	if err := createIndex(ctx, s, db, coll, spec); err != nil {
		if restoreErr := createIndex(context.WithoutCancel(ctx), s, db, coll, creationOptions(current)); restoreErr != nil {
			return fmt.Errorf("%w; restoring the previous index also failed: %w", err, restoreErr)
		}
		return fmt.Errorf("%w (the previous index was restored)", err)
	}
	return nil
}

func createIndex(ctx context.Context, s *docsession.Session, db, coll string, spec bson.D) error {
	return s.Client.Database(db).RunCommand(ctx, bson.D{{Key: "createIndexes", Value: coll}, {Key: "indexes", Value: bson.A{spec}}}).Err()
}

// parseIndexSpec validates an index definition and fills in a default name.
func parseIndexSpec(text string) (bson.D, error) {
	spec, err := shell.EvalDoc(text, "index")
	if err != nil {
		return nil, err
	}
	keyVal, _ := find(spec, "key")
	key, ok := keyVal.(bson.D)
	if !ok || len(key) == 0 {
		return nil, errors.New("the index needs at least one key field")
	}
	for _, k := range key {
		if strings.TrimSpace(k.Key) == "" {
			return nil, errors.New("index key fields must have a name")
		}
	}
	name, _ := find(spec, "name")
	if n, _ := name.(string); strings.TrimSpace(n) == "" {
		spec = slices.DeleteFunc(spec, func(e bson.E) bool { return e.Key == "name" })
		spec = append(spec, bson.E{Key: "name", Value: defaultIndexName(key)})
	}
	return spec, nil
}

// defaultIndexName follows MongoDB's convention: field_direction joined by "_".
func defaultIndexName(key bson.D) string {
	parts := make([]string, 0, len(key)*2)
	for _, k := range key {
		parts = append(parts, k.Key, strings.Trim(shell.Format(k.Value, ""), `"`))
	}
	return strings.Join(parts, "_")
}

// creationOptions strips the fields listIndexes adds so a spec can be created again.
func creationOptions(spec bson.D) bson.D {
	out := make(bson.D, 0, len(spec))
	for _, e := range spec {
		if !slices.Contains(serverOnlyFields, e.Key) {
			out = append(out, e)
		}
	}
	return out
}

// diffIndex lists the options that differ between the current and the wanted spec,
// and whether all of them can be changed in place with collMod.
func diffIndex(current, wanted bson.D) (changed []string, inPlace bool) {
	cur, want := creationOptions(textKeyAsFields(current)), creationOptions(textKeyAsFields(wanted))
	keys := map[string]bool{}
	for _, e := range cur {
		keys[e.Key] = true
	}
	for _, e := range want {
		keys[e.Key] = true
	}
	inPlace = true
	for k := range keys {
		a, _ := find(cur, k)
		b, _ := find(want, k)
		if sameValue(a, b) || (k == "hidden" && isFalse(a) && isFalse(b)) {
			continue
		}
		changed = append(changed, k)
		if !slices.Contains(collModFields, k) {
			inPlace = false
		}
	}
	slices.Sort(changed)
	return changed, inPlace
}

func isFalse(v any) bool {
	b, ok := v.(bool)
	return v == nil || (ok && !b)
}

// sameValue compares values by their meaning: 1, NumberLong(1) and 1.0 are equal.
func sameValue(a, b any) bool {
	if na, ok := number(a); ok {
		nb, ok := number(b)
		return ok && na == nb
	}
	x, err1 := shell.CanonicalValue(normalizeNumbers(a))
	y, err2 := shell.CanonicalValue(normalizeNumbers(b))
	return err1 == nil && err2 == nil && x == y
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

func normalizeNumbers(v any) any {
	switch x := v.(type) {
	case bson.D:
		out := make(bson.D, len(x))
		for i, e := range x {
			out[i] = bson.E{Key: e.Key, Value: normalizeNumbers(e.Value)}
		}
		return out
	case bson.A:
		out := make(bson.A, len(x))
		for i, e := range x {
			out[i] = normalizeNumbers(e)
		}
		return out
	}
	if n, ok := number(v); ok {
		return n
	}
	return v
}

// textKeyAsFields rewrites the internal key of a text index ({_fts: "text", _ftsx: 1},
// as listIndexes reports it) into the fields it was created with ({title: "text"}),
// taken from the weights. Other specs are returned unchanged.
func textKeyAsFields(spec bson.D) bson.D {
	keyVal, _ := find(spec, "key")
	key, ok := keyVal.(bson.D)
	if !ok || !slices.ContainsFunc(key, func(e bson.E) bool { return e.Key == "_fts" }) {
		return spec
	}
	weightsVal, _ := find(spec, "weights")
	weights, _ := weightsVal.(bson.D)
	out := make(bson.D, 0, len(key)+len(weights))
	for _, k := range key {
		switch k.Key {
		case "_fts":
			for _, w := range weights {
				out = append(out, bson.E{Key: w.Key, Value: "text"})
			}
		case "_ftsx":
		default:
			out = append(out, k)
		}
	}
	res := make(bson.D, len(spec))
	copy(res, spec)
	for i, e := range res {
		if e.Key == "key" {
			res[i].Value = out
		}
	}
	return res
}
