package shell

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// QuerySpec is a re-runnable read query with BSON values.
type QuerySpec struct {
	Kind       string // find | aggregate
	Database   string
	Collection string
	Filter     bson.D
	Projection bson.D
	Sort       bson.D
	Pipeline   bson.A
	Skip       int64
	Limit      int64
	Hint       any
}

// Page is one page of results.
type Page struct {
	// Docs are canonical Extended JSON documents.
	Docs       []string `json:"docs"`
	Offset     int64    `json:"offset"`
	HasMore    bool     `json:"hasMore"`
	DurationMs int64    `json:"durationMs"`
}

// FetchPage reads pageSize documents starting at offset (relative to the spec's own skip).
func FetchPage(ctx context.Context, client *mongo.Client, q QuerySpec, offset, pageSize int64) (Page, error) {
	start := time.Now()
	want := pageSize
	if q.Limit > 0 {
		if remaining := q.Limit - offset; remaining < want {
			want = max(remaining, 0)
		}
	}
	page := Page{Offset: offset, Docs: []string{}}
	if want == 0 {
		return page, nil
	}
	coll := client.Database(q.Database).Collection(q.Collection)
	var (
		cur *mongo.Cursor
		err error
	)
	switch q.Kind {
	case "aggregate":
		pipe := append(bson.A{}, q.Pipeline...)
		if q.Skip+offset > 0 {
			pipe = append(pipe, bson.D{{Key: "$skip", Value: q.Skip + offset}})
		}
		pipe = append(pipe, bson.D{{Key: "$limit", Value: want + 1}})
		cur, err = coll.Aggregate(ctx, pipe)
	default:
		o := options.Find().SetSkip(q.Skip + offset).SetLimit(want + 1)
		if len(q.Projection) > 0 {
			o.SetProjection(q.Projection)
		}
		if len(q.Sort) > 0 {
			o.SetSort(q.Sort)
		}
		if q.Hint != nil {
			o.SetHint(q.Hint)
		}
		filter := q.Filter
		if filter == nil {
			filter = bson.D{}
		}
		cur, err = coll.Find(ctx, filter, o)
	}
	if err != nil {
		return page, err
	}
	defer func() { _ = cur.Close(context.WithoutCancel(ctx)) }()
	for cur.Next(ctx) {
		if int64(len(page.Docs)) == want {
			page.HasMore = q.Limit <= 0 || offset+want < q.Limit
			break
		}
		raw, err := bson.MarshalExtJSON(cur.Current, true, false)
		if err != nil {
			return page, err
		}
		page.Docs = append(page.Docs, string(raw))
	}
	if err := cur.Err(); err != nil {
		return page, err
	}
	page.DurationMs = time.Since(start).Milliseconds()
	return page, nil
}

// Count returns the number of documents matched by a query, honouring skip and limit.
func Count(ctx context.Context, client *mongo.Client, q QuerySpec) (int64, error) {
	coll := client.Database(q.Database).Collection(q.Collection)
	if q.Kind == "aggregate" {
		pipe := append(bson.A{}, q.Pipeline...)
		if q.Skip > 0 {
			pipe = append(pipe, bson.D{{Key: "$skip", Value: q.Skip}})
		}
		if q.Limit > 0 {
			pipe = append(pipe, bson.D{{Key: "$limit", Value: q.Limit}})
		}
		pipe = append(pipe, bson.D{{Key: "$count", Value: "n"}})
		cur, err := coll.Aggregate(ctx, pipe)
		if err != nil {
			return 0, err
		}
		defer func() { _ = cur.Close(context.WithoutCancel(ctx)) }()
		var res []struct {
			N int64 `bson:"n"`
		}
		if err := cur.All(ctx, &res); err != nil || len(res) == 0 {
			return 0, err
		}
		return res[0].N, nil
	}
	o := options.Count()
	if q.Skip > 0 {
		o.SetSkip(q.Skip)
	}
	if q.Limit > 0 {
		o.SetLimit(q.Limit)
	}
	if q.Hint != nil {
		o.SetHint(q.Hint)
	}
	filter := q.Filter
	if filter == nil {
		filter = bson.D{}
	}
	if len(filter) == 0 && q.Skip == 0 && q.Limit == 0 {
		return coll.EstimatedDocumentCount(ctx)
	}
	return coll.CountDocuments(ctx, filter, o)
}
