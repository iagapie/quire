package docapp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/iagapie/quire/internal/docdump"
	"github.com/iagapie/quire/internal/profile"
)

func TestScanImportFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("orders.jsonl", `{"a":1}`)
	write("users.bson.gz", "x")
	write("notes.txt", "x")
	write(".hidden.json", "{}")
	meta, err := docdump.EncodeMetadata(docdump.Metadata{
		CollectionName: "real/orders",
		Indexes: []bson.D{
			{{Key: "key", Value: bson.D{{Key: "_id", Value: 1}}}, {Key: "name", Value: "_id_"}},
			{{Key: "key", Value: bson.D{{Key: "sku", Value: 1}}}, {Key: "name", Value: "sku_1"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	write("orders.metadata.json", string(meta))
	other := write("extra.json", "[]")
	bad := filepath.Join(t.TempDir(), "readme.md")
	if err := os.WriteFile(bad, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	scan := (&Transfers{}).ScanImportFiles([]string{dir, other, bad, filepath.Join(dir, "missing.json")})
	var names []string
	for _, f := range scan.Files {
		names = append(names, f.Name+"→"+f.Collection)
	}
	want := []string{"extra.json→extra", "orders.jsonl→real/orders", "users.bson.gz→users"}
	if !slices.Equal(names, want) {
		t.Errorf("files = %v, want %v", names, want)
	}
	if !slices.Equal(scan.Skipped, []string{"readme.md", "missing.json"}) {
		t.Errorf("skipped = %v", scan.Skipped)
	}
	for _, f := range scan.Files {
		switch f.Name {
		case "orders.jsonl":
			if f.Indexes != 1 || f.Metadata == "" || f.Format != "jsonl" {
				t.Errorf("orders = %+v", f)
			}
		case "users.bson.gz":
			if !f.Gzip || f.Format != "bson" || f.Size != 1 {
				t.Errorf("users = %+v", f)
			}
		}
	}
}

func TestStartValidation(t *testing.T) {
	store, err := profile.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tr := NewTransfers(newConns(store).docs)
	if _, err := tr.StartExport(ExportRequest{SessionID: "x"}); !errors.Is(err, ErrNotConnected) {
		t.Errorf("export without session: %v", err)
	}
	if _, err := tr.StartImport(ImportRequest{SessionID: "x"}); !errors.Is(err, ErrNotConnected) {
		t.Errorf("import without session: %v", err)
	}
	if _, err := tr.Job("nope"); err == nil {
		t.Error("unknown job")
	}
	if err := tr.Cancel("nope"); err == nil {
		t.Error("cancel unknown job")
	}
	if err := tr.Remove("nope"); err != nil {
		t.Errorf("remove unknown job: %v", err)
	}
	if len(tr.Jobs()) != 0 {
		t.Error("no jobs expected")
	}
	if plural(1, "a", "b") != "a" || plural(2, "a", "b") != "b" {
		t.Error("plural")
	}
	if joinWarning("a", "") != "a" || joinWarning("", "b") != "b" || joinWarning("a", "b") != "a; b" {
		t.Error("joinWarning")
	}
	if retryable(context.Background(), errors.New("x")) || !retryable(context.Background(), context.DeadlineExceeded) {
		t.Error("retryable")
	}
}

func waitJob(t *testing.T, tr *Transfers, id string) TransferJob {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		j, err := tr.Job(id)
		if err != nil {
			t.Fatal(err)
		}
		if j.State != stateRunning {
			return j
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("transfer did not finish")
	return TransferJob{}
}

func seedTransfer(t *testing.T, client *mongo.Client) []bson.Raw {
	t.Helper()
	ctx := context.Background()
	db := client.Database("xfer")
	if err := db.Drop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.Database("xfer_in").Drop(ctx); err != nil {
		t.Fatal(err)
	}
	var docs []any
	for i := range 2500 {
		var id any = int32(i)
		if i%10 == 0 {
			id = fmt.Sprintf("s%04d", i) // mixed _id types still export in a stable order
		}
		docs = append(docs, bson.D{
			{Key: "_id", Value: id},
			{Key: "n", Value: int64(i)},
			{Key: "f", Value: float64(i) / 3},
			{Key: "at", Value: bson.NewDateTimeFromTime(time.Unix(int64(i), 0))},
			{Key: "tags", Value: bson.A{"a", int32(i % 7)}},
		})
	}
	if _, err := db.Collection("big").InsertMany(ctx, docs); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Collection("big").Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "n", Value: -1}}, Options: options.Index().SetName("n_desc")}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Collection("small").InsertOne(ctx, bson.D{{Key: "_id", Value: 1}, {Key: "x", Value: "y"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateView(ctx, "small_view", "small", bson.A{}); err != nil {
		t.Fatal(err)
	}
	return allDocs(t, client, "xfer", "big")
}

func allDocs(t *testing.T, client *mongo.Client, db, coll string) []bson.Raw {
	t.Helper()
	cur, err := client.Database(db).Collection(coll).Find(context.Background(), bson.D{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		t.Fatal(err)
	}
	var out []bson.Raw
	if err := cur.All(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestIntegrationTransfers(t *testing.T) {
	c, _ := newTestConns(t)
	ctx := context.Background()
	id := save(t, c, profile.Profile{Name: "root", User: "root", Password: "rootpw"})
	if _, err := c.Connect(ctx, id); err != nil {
		t.Fatal(err)
	}
	s, _ := c.get(id)
	want := seedTransfer(t, s.Client)
	tr := NewTransfers(c.docs)
	t.Cleanup(func() { _ = tr.ServiceShutdown() })

	stats, err := tr.CollectionStats(ctx, id, "xfer")
	if err != nil || len(stats) != 3 || stats[0].Name != "big" || stats[0].Count != 2500 || stats[0].Size <= 0 || stats[2].Type != "view" || stats[2].Count != -1 {
		t.Fatalf("stats = %+v, %v", stats, err)
	}

	// A network failure in the middle of an export resumes where it stopped.
	var failed atomic.Bool
	exportFault = func(n int64) error {
		if n == 1234 && failed.CompareAndSwap(false, true) {
			return &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")}
		}
		return nil
	}
	t.Cleanup(func() { exportFault = nil })
	oldBackoff := transferBackoff
	transferBackoff = 10 * time.Millisecond
	t.Cleanup(func() { transferBackoff = oldBackoff })

	for _, tc := range []struct {
		format    string
		gzip      bool
		canonical bool
		mode      string
	}{
		{"jsonl", false, true, ImportInsert},
		{"json", true, true, ImportUpsert},
		{"bson", true, false, ImportReplace},
		{"bson", false, false, ImportInsert},
	} {
		name := fmt.Sprintf("%s-gz%v-%s", tc.format, tc.gzip, tc.mode)
		dir := t.TempDir()
		j, err := tr.StartExport(ExportRequest{SessionID: id, Database: "xfer", Collections: []string{"big", "small", "small_view"}, Dir: dir, Format: tc.format, Canonical: tc.canonical, Gzip: tc.gzip, Indexes: true})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		j = waitJob(t, tr, j.ID)
		if j.State != stateDone {
			t.Fatalf("%s: export %+v", name, j)
		}
		if it := j.Items[0]; it.Docs != 2500 || it.Total != 2500 || it.Bytes <= 0 || it.Info != "2 indexes saved" {
			t.Errorf("%s: big item = %+v", name, it)
		}
		if j.Items[2].Docs != 1 || j.Items[2].Info != "" {
			t.Errorf("%s: view item = %+v", name, j.Items[2])
		}
		entries, _ := os.ReadDir(dir)
		var files []string
		for _, e := range entries {
			files = append(files, e.Name())
		}
		if len(files) != 5 || slices.ContainsFunc(files, func(f string) bool { return strings.HasSuffix(f, ".part") }) {
			t.Errorf("%s: files = %v", name, files)
		}

		scan := tr.ScanImportFiles([]string{dir})
		var in []ImportFile
		for _, f := range scan.Files {
			if f.Collection == "big" {
				in = append(in, f)
			}
		}
		if len(in) != 1 || in[0].Indexes != 1 {
			t.Fatalf("%s: scan = %+v", name, scan)
		}
		if tc.mode != ImportReplace {
			// Pre-existing documents: insert skips them, upsert replaces them.
			_ = s.Client.Database("xfer_in").Collection("big").Drop(ctx)
			_, _ = s.Client.Database("xfer_in").Collection("big").InsertOne(ctx, bson.D{{Key: "_id", Value: int32(1)}, {Key: "old", Value: true}})
		}
		j, err = tr.StartImport(ImportRequest{SessionID: id, Database: "xfer_in", Files: in, Mode: tc.mode, Indexes: true})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		j = waitJob(t, tr, j.ID)
		it := j.Items[0]
		if j.State != stateDone || it.Docs != 2500 || it.Failed != 0 || it.Bytes != it.TotalBytes || it.Info != "1 index restored" {
			t.Fatalf("%s: import %+v", name, j)
		}
		got := allDocs(t, s.Client, "xfer_in", "big")
		if len(got) != len(want) {
			t.Fatalf("%s: %d docs imported", name, len(got))
		}
		mismatches := 0
		for k := range want {
			if string(got[k]) != string(want[k]) {
				mismatches++
			}
		}
		switch tc.mode {
		case ImportInsert:
			if it.Skipped != 1 || mismatches != 1 {
				t.Errorf("%s: skipped %d, mismatches %d", name, it.Skipped, mismatches)
			}
		default:
			// Relaxed JSON is not used here, so every value and type round-trips.
			if mismatches != 0 {
				t.Errorf("%s: %d documents differ", name, mismatches)
			}
		}
		if ix, _ := c.ListIndexes(ctx, id, "xfer_in", "big"); len(ix) != 2 {
			t.Errorf("%s: indexes %+v", name, ix)
		}
		_ = tr.Remove(j.ID)
	}
	if !failed.Load() {
		t.Error("the fault was never injected")
	}

	// Cancelling stops the transfer and leaves no partial files.
	exportFault = func(int64) error { time.Sleep(time.Millisecond); return nil }
	dir := t.TempDir()
	j, err := tr.StartExport(ExportRequest{SessionID: id, Database: "xfer", Collections: []string{"big", "small"}, Dir: dir, Format: "jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := tr.Remove(j.ID); err == nil {
		t.Error("removing a running job must fail")
	}
	if err := tr.Cancel(j.ID); err != nil {
		t.Fatal(err)
	}
	j = waitJob(t, tr, j.ID)
	if j.State != stateCancelled || j.Items[0].State != stateCancelled {
		t.Errorf("cancelled job = %+v", j)
	}
	if entries, _ := os.ReadDir(dir); len(entries) > 1 {
		t.Errorf("files left after cancel: %v", entries)
	}
	exportFault = nil

	// Retry runs what did not finish.
	j, err = tr.Retry(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	j = waitJob(t, tr, j.ID)
	if j.State != stateDone || j.Items[0].Docs != 2500 {
		t.Errorf("retried job = %+v", j)
	}
	if len(tr.Jobs()) == 0 {
		t.Error("Jobs is empty")
	}

	// Bad input fails the item with a clear error; the other files still import.
	bad := filepath.Join(t.TempDir(), "bad.jsonl")
	_ = os.WriteFile(bad, []byte("{\"a\":1}\n{nope"), 0o600)
	good := filepath.Join(dir, "small.jsonl")
	j, err = tr.StartImport(ImportRequest{SessionID: id, Database: "xfer_in", Mode: ImportInsert, Files: []ImportFile{
		{Path: bad, Name: "bad.jsonl", Format: "jsonl", Collection: "bad"},
		{Path: good, Name: "small.jsonl", Format: "jsonl", Collection: "small2"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	j = waitJob(t, tr, j.ID)
	if j.State != stateFailed || j.Items[0].State != stateFailed || !strings.Contains(j.Items[0].Error, "document 2") || j.Items[1].State != stateDone {
		t.Errorf("bad import = %+v", j)
	}

	for _, req := range []ImportRequest{
		{SessionID: id, Database: "x", Mode: "nope", Files: []ImportFile{{}}},
		{SessionID: id, Database: "x"},
		{SessionID: id, Database: "x", Files: []ImportFile{{Name: "a", Format: "jsonl"}}},
		{SessionID: id, Database: "x", Files: []ImportFile{{Name: "a", Format: "csv", Collection: "a"}}},
		{SessionID: id, Database: "x", Files: []ImportFile{{Name: "a", Format: "jsonl", Collection: "system.users"}}},
	} {
		if _, err := tr.StartImport(req); err == nil {
			t.Errorf("import %+v should fail", req)
		}
	}
	for _, req := range []ExportRequest{
		{SessionID: id, Database: "x", Format: "csv", Collections: []string{"a"}, Dir: dir},
		{SessionID: id, Database: "x", Format: "json", Dir: dir},
		{SessionID: id, Database: "x", Format: "json", Collections: []string{"a"}},
	} {
		if _, err := tr.StartExport(req); err == nil {
			t.Errorf("export %+v should fail", req)
		}
	}

	ro := save(t, c, profile.Profile{Name: "ro", User: "root", Password: "rootpw", ReadOnly: true})
	if _, err := c.Connect(ctx, ro); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.StartImport(ImportRequest{SessionID: ro, Database: "x", Files: []ImportFile{{Name: "a", Format: "jsonl", Collection: "a"}}}); !errors.Is(err, errReadOnly) {
		t.Errorf("read-only import: %v", err)
	}
}
