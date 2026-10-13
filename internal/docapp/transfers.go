package docapp

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/iagapie/quire/internal/docdump"
	"github.com/iagapie/quire/internal/docsession"
)

// Events of the transfer service.
const (
	// EventTransfer carries a TransferJob snapshot whenever an export or import progresses.
	EventTransfer = "doc-transfer:progress"
	// EventFilesDropped carries the dumps dropped onto an import tab.
	EventFilesDropped = "doc-import:dropped"
)

// FilesDropped reports files dropped from the OS onto an element marked with
// data-file-drop-target; Target is that element's id.
type FilesDropped struct {
	Target string     `json:"target"`
	Scan   ImportScan `json:"scan"`
}

func init() {
	application.RegisterEvent[TransferJob](EventTransfer)
	application.RegisterEvent[FilesDropped](EventFilesDropped)
}

// FilesDropped inspects files dropped onto the window and forwards the dumps among them.
func (t *Transfers) FilesDropped(paths []string, target string) {
	emit(EventFilesDropped, FilesDropped{Target: target, Scan: t.ScanImportFiles(paths)})
}

// Job and item states.
const (
	statePending   = "pending"
	stateRunning   = "running"
	stateDone      = "done"
	stateFailed    = "failed"
	stateCancelled = "cancelled"
)

// Import modes.
const (
	ImportInsert  = "insert"  // insert, skipping documents whose _id already exists
	ImportUpsert  = "upsert"  // replace documents with the same _id, insert the rest
	ImportReplace = "replace" // drop the collection first
)

// ExportRequest describes an export of collections to a directory, one file each.
type ExportRequest struct {
	SessionID   string   `json:"sessionId"`
	Database    string   `json:"database"`
	Collections []string `json:"collections"`
	Dir         string   `json:"dir"`
	// Format is "jsonl", "json" or "bson".
	Format string `json:"format"`
	// Canonical selects lossless canonical Extended JSON over relaxed.
	Canonical bool `json:"canonical"`
	Gzip      bool `json:"gzip"`
	// Indexes writes a mongodump-style <collection>.metadata.json with the indexes.
	Indexes bool `json:"indexes"`
}

// ImportFile is a dump file and the collection it goes into.
type ImportFile struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Format     string `json:"format"`
	Gzip       bool   `json:"gzip"`
	Collection string `json:"collection"`
	// Metadata is the path of a matching mongodump metadata file, if any.
	Metadata string `json:"metadata"`
	// Indexes is the number of indexes (besides _id) in the metadata.
	Indexes int `json:"indexes"`
}

// ImportScan is the result of inspecting files picked for an import.
type ImportScan struct {
	Files []ImportFile `json:"files"`
	// Skipped lists picked files that are not dumps.
	Skipped []string `json:"skipped"`
}

// ImportRequest describes an import of dump files into a database.
type ImportRequest struct {
	SessionID string       `json:"sessionId"`
	Database  string       `json:"database"`
	Files     []ImportFile `json:"files"`
	Mode      string       `json:"mode"`
	// Indexes creates the indexes listed in the metadata files.
	Indexes     bool `json:"indexes"`
	StopOnError bool `json:"stopOnError"`
}

// TransferItem is the progress of one collection.
type TransferItem struct {
	Collection string `json:"collection"`
	File       string `json:"file"`
	State      string `json:"state"`
	Docs       int64  `json:"docs"`
	// Total is the expected number of documents, or -1 if unknown.
	Total      int64 `json:"total"`
	Bytes      int64 `json:"bytes"`
	TotalBytes int64 `json:"totalBytes"`
	// Skipped counts documents not imported because their _id already exists.
	Skipped int64 `json:"skipped"`
	// Failed counts documents the server rejected.
	Failed  int64  `json:"failed"`
	Error   string `json:"error"`
	Warning string `json:"warning"`
	// Info is a note about extra work done, such as restored indexes.
	Info      string `json:"info"`
	Retry     string `json:"retry"`
	ElapsedMs int64  `json:"elapsedMs"`

	started time.Time
}

// TransferJob is an export or import running in the background.
type TransferJob struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	SessionID string         `json:"sessionId"`
	Database  string         `json:"database"`
	Dir       string         `json:"dir"`
	Format    string         `json:"format"`
	Mode      string         `json:"mode"`
	State     string         `json:"state"`
	StartedAt string         `json:"startedAt"`
	ElapsedMs int64          `json:"elapsedMs"`
	Items     []TransferItem `json:"items"`
}

// CollectionStat is the size of a collection, for choosing what to export.
type CollectionStat struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Count and Size are -1 when unknown (views, missing privileges).
	Count int64 `json:"count"`
	Size  int64 `json:"size"`
}

type job struct {
	mu sync.Mutex
	TransferJob
	export  *ExportRequest
	imp     *ImportRequest
	started time.Time
	cancel  context.CancelFunc
	dirty   bool
	done    chan struct{}
}

// Transfers exports and imports collections in the background, reporting progress
// with EventTransfer.
type Transfers struct {
	sessions *docsession.Registry

	mu   sync.Mutex
	jobs map[string]*job
	seq  int
}

// NewTransfers creates the service.
func NewTransfers(sessions *docsession.Registry) *Transfers {
	return &Transfers{sessions: sessions, jobs: map[string]*job{}}
}

// ServiceShutdown cancels running transfers and waits briefly for them to clean up.
func (t *Transfers) ServiceShutdown() error {
	t.mu.Lock()
	jobs := make([]*job, 0, len(t.jobs))
	for _, j := range t.jobs {
		jobs = append(jobs, j)
	}
	t.mu.Unlock()
	for _, j := range jobs {
		j.mu.Lock()
		cancel, done := j.cancel, j.done
		j.mu.Unlock()
		if cancel != nil {
			cancel()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
			}
		}
	}
	return nil
}

// Tunables, variables for tests.
var (
	transferBackoff    = 2 * time.Second
	transferMaxRetries = 8
	exportBatchSize    = int32(1000)
	importBatchDocs    = 1000
	importBatchBytes   = 8 << 20
	progressInterval   = 250 * time.Millisecond
	batchTimeout       = 60 * time.Second
	// exportFault, when set by tests, can fail the export after a document is written.
	exportFault func(written int64) error
)

// Jobs returns all known transfers, newest first.
func (t *Transfers) Jobs() []TransferJob {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]TransferJob, 0, len(t.jobs))
	for _, j := range t.jobs {
		out = append(out, j.snapshot())
	}
	slices.SortFunc(out, func(a, b TransferJob) int { return strings.Compare(b.ID, a.ID) })
	return out
}

// Job returns one docdump.
func (t *Transfers) Job(id string) (TransferJob, error) {
	j, err := t.job(id)
	if err != nil {
		return TransferJob{}, err
	}
	return j.snapshot(), nil
}

func (t *Transfers) job(id string) (*job, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	j, ok := t.jobs[id]
	if !ok {
		return nil, fmt.Errorf("transfer %s not found", id)
	}
	return j, nil
}

// Cancel stops a running docdump. Finished collections are kept.
func (t *Transfers) Cancel(id string) error {
	j, err := t.job(id)
	if err != nil {
		return err
	}
	j.mu.Lock()
	cancel := j.cancel
	j.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// Remove forgets a finished docdump.
func (t *Transfers) Remove(id string) error {
	j, err := t.job(id)
	if err != nil {
		return nil //nolint:nilerr // already gone
	}
	j.mu.Lock()
	running := j.State == stateRunning
	j.mu.Unlock()
	if running {
		return errors.New("the transfer is still running; cancel it first")
	}
	t.mu.Lock()
	delete(t.jobs, id)
	t.mu.Unlock()
	return nil
}

// Retry runs the failed and cancelled collections of a finished transfer again.
func (t *Transfers) Retry(id string) (TransferJob, error) {
	j, err := t.job(id)
	if err != nil {
		return TransferJob{}, err
	}
	s, err := t.sessions.Get(j.SessionID)
	if err != nil {
		return TransferJob{}, err
	}
	j.mu.Lock()
	if j.State == stateRunning {
		j.mu.Unlock()
		return j.snapshot(), nil
	}
	var todo []int
	for i := range j.Items {
		it := &j.Items[i]
		if it.State == stateFailed || it.State == stateCancelled {
			*it = TransferItem{Collection: it.Collection, File: it.File, State: statePending, Total: -1, TotalBytes: it.TotalBytes}
			todo = append(todo, i)
		}
	}
	j.mu.Unlock()
	if len(todo) == 0 {
		return j.snapshot(), nil
	}
	t.start(s, j, todo)
	return j.snapshot(), nil
}

// StartExport validates an export and starts it in the background.
func (t *Transfers) StartExport(req ExportRequest) (TransferJob, error) {
	s, err := t.sessions.Get(req.SessionID)
	if err != nil {
		return TransferJob{}, err
	}
	f := docdump.Format(req.Format)
	switch {
	case !f.Valid():
		return TransferJob{}, fmt.Errorf("unknown format %q", req.Format)
	case len(req.Collections) == 0:
		return TransferJob{}, errors.New("choose at least one collection")
	case req.Dir == "":
		return TransferJob{}, errors.New("choose a destination folder")
	}
	if err := os.MkdirAll(req.Dir, 0o750); err != nil {
		return TransferJob{}, fmt.Errorf("destination folder: %w", err)
	}
	if err := checkWritable(req.Dir); err != nil {
		return TransferJob{}, err
	}
	j := t.newJob("export", req.SessionID, req.Database)
	j.export = &req
	j.Dir, j.Format = req.Dir, req.Format
	for _, c := range req.Collections {
		j.Items = append(j.Items, TransferItem{Collection: c, File: docdump.FileName(c, f, req.Gzip), State: statePending, Total: -1})
	}
	t.start(s, j, nil)
	return j.snapshot(), nil
}

// StartImport validates an import and starts it in the background.
func (t *Transfers) StartImport(req ImportRequest) (TransferJob, error) {
	s, err := t.sessions.Get(req.SessionID)
	if err != nil {
		return TransferJob{}, err
	}
	if s.Profile.ReadOnly {
		return TransferJob{}, errReadOnly
	}
	if req.Mode == "" {
		req.Mode = ImportInsert
	}
	if req.Mode != ImportInsert && req.Mode != ImportUpsert && req.Mode != ImportReplace {
		return TransferJob{}, fmt.Errorf("unknown import mode %q", req.Mode)
	}
	if len(req.Files) == 0 {
		return TransferJob{}, errors.New("add at least one file")
	}
	for i, f := range req.Files {
		req.Files[i].Collection = strings.TrimSpace(f.Collection)
		if req.Files[i].Collection == "" {
			return TransferJob{}, fmt.Errorf("%s: choose a target collection", f.Name)
		}
		if strings.HasPrefix(req.Files[i].Collection, "system.") || strings.ContainsRune(req.Files[i].Collection, '$') {
			return TransferJob{}, fmt.Errorf("%q is not a valid collection name", req.Files[i].Collection)
		}
		if !docdump.Format(f.Format).Valid() {
			return TransferJob{}, fmt.Errorf("%s: unsupported file", f.Name)
		}
	}
	j := t.newJob("import", req.SessionID, req.Database)
	j.imp = &req
	j.Mode = req.Mode
	for _, f := range req.Files {
		j.Items = append(j.Items, TransferItem{Collection: f.Collection, File: f.Name, State: statePending, Total: -1, TotalBytes: f.Size})
	}
	t.start(s, j, nil)
	return j.snapshot(), nil
}

func checkWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".quire-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s: %w", dir, err)
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

func (t *Transfers) newJob(kind, sessionID, db string) *job {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	now := time.Now()
	j := &job{TransferJob: TransferJob{
		ID:        fmt.Sprintf("doc-%s-%s-%03d", kind, now.Format("20060102T150405"), t.seq),
		Kind:      kind,
		SessionID: sessionID,
		Database:  db,
	}}
	t.jobs[j.ID] = j
	return j
}

func (j *job) snapshot() TransferJob {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.snapshotLocked()
}

func (j *job) snapshotLocked() TransferJob {
	out := j.TransferJob
	out.Items = slices.Clone(j.Items)
	if j.State == stateRunning {
		out.ElapsedMs += time.Since(j.started).Milliseconds()
	}
	for i := range out.Items {
		if out.Items[i].State == stateRunning {
			out.Items[i].ElapsedMs = time.Since(out.Items[i].started).Milliseconds()
		}
	}
	return out
}

func (j *job) update(i int, fn func(*TransferItem)) {
	j.mu.Lock()
	fn(&j.Items[i])
	j.dirty = true
	j.mu.Unlock()
}

// start runs the given items (all when nil) with a few workers, emitting progress.
func (t *Transfers) start(s *docsession.Session, j *job, items []int) {
	ctx, cancel := context.WithCancel(context.Background())
	j.mu.Lock()
	if items == nil {
		for i := range j.Items {
			items = append(items, i)
		}
	}
	j.State = stateRunning
	j.started = time.Now()
	j.StartedAt = j.started.UTC().Format(time.RFC3339)
	j.cancel = cancel
	j.done = make(chan struct{})
	j.dirty = true
	done := j.done
	j.mu.Unlock()

	workers := 3
	if j.imp != nil {
		workers = 2
		// Files going into the same collection must not race (a drop would wipe the other).
		seen := map[string]bool{}
		for _, f := range j.imp.Files {
			if seen[f.Collection] {
				workers = 1
			}
			seen[f.Collection] = true
		}
	}
	workers = min(workers, len(items))

	go func() {
		defer close(done)
		defer cancel()
		stop := make(chan struct{})
		go j.emitLoop(stop)

		var views map[string]bool
		if j.export != nil {
			views = viewNames(ctx, s, j.Database)
		}
		queue := make(chan int)
		var wg sync.WaitGroup
		for range workers {
			wg.Go(func() {
				for i := range queue {
					t.runItem(ctx, s, j, i, views)
				}
			})
		}
		for _, i := range items {
			if ctx.Err() != nil {
				break
			}
			queue <- i
		}
		close(queue)
		wg.Wait()
		close(stop)

		j.mu.Lock()
		state := stateDone
		for i := range j.Items {
			it := &j.Items[i]
			if it.State == statePending || it.State == stateRunning {
				it.State = stateCancelled
			}
			switch it.State {
			case stateFailed:
				state = stateFailed
			case stateCancelled:
				if state != stateFailed {
					state = stateCancelled
				}
			}
		}
		j.State = state
		j.ElapsedMs += time.Since(j.started).Milliseconds()
		j.cancel = nil
		snap := j.snapshotLocked()
		j.mu.Unlock()
		emit(EventTransfer, snap)
	}()
}

func (j *job) emitLoop(stop chan struct{}) {
	tick := time.NewTicker(progressInterval)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			j.mu.Lock()
			if !j.dirty {
				j.mu.Unlock()
				continue
			}
			j.dirty = false
			snap := j.snapshotLocked()
			j.mu.Unlock()
			emit(EventTransfer, snap)
		}
	}
}

func (t *Transfers) runItem(ctx context.Context, s *docsession.Session, j *job, i int, views map[string]bool) {
	if ctx.Err() != nil {
		return
	}
	j.update(i, func(it *TransferItem) {
		it.State = stateRunning
		it.started = time.Now()
	})
	var err error
	if j.export != nil {
		err = exportCollection(ctx, s, j, i, views[j.Items[i].Collection])
	} else {
		err = importFile(ctx, s, j, i)
	}
	j.update(i, func(it *TransferItem) {
		it.ElapsedMs = time.Since(it.started).Milliseconds()
		it.Retry = ""
		switch {
		case err == nil:
			it.State = stateDone
		case ctx.Err() != nil:
			it.State = stateCancelled
		default:
			it.State = stateFailed
			it.Error = err.Error()
		}
	})
}

func viewNames(ctx context.Context, s *docsession.Session, db string) map[string]bool {
	out := map[string]bool{}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	colls, err := docsession.RetryRead(cctx, nil, func(ctx context.Context) ([]docsession.Collection, error) { return s.ListCollections(ctx, db) })
	if err == nil {
		for _, c := range colls {
			out[c.Name] = c.Type == "view"
		}
	}
	return out
}

func opTimeout(s *docsession.Session) time.Duration {
	return time.Duration(s.Profile.QueryTimeoutMs) * time.Millisecond
}

// retryable reports whether a transfer step should be retried after err: network
// failures, and per-operation timeouts while the transfer itself is still wanted.
func retryable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	return docsession.IsTransient(err) || errors.Is(err, context.DeadlineExceeded)
}

// withRetry runs fn until it succeeds, fails permanently or runs out of attempts,
// reporting retries on the item.
func withRetry(ctx context.Context, j *job, i int, what func(attempt int) string, fn func(context.Context) error) error {
	for attempt := 1; ; attempt++ {
		err := fn(ctx)
		if err == nil {
			if attempt > 1 {
				j.update(i, func(it *TransferItem) { it.Retry = "" })
			}
			return nil
		}
		if !retryable(ctx, err) || attempt > transferMaxRetries {
			return err
		}
		msg := fmt.Sprintf("%s (attempt %d of %d): %v", what(attempt), attempt, transferMaxRetries, err)
		j.update(i, func(it *TransferItem) { it.Retry = msg })
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(min(time.Duration(attempt)*transferBackoff, 15*time.Second)):
		}
	}
}

func exportCollection(ctx context.Context, s *docsession.Session, j *job, i int, view bool) (err error) {
	req := j.export
	name := j.Items[i].Collection
	coll := s.Client.Database(j.Database).Collection(name)
	timeout := opTimeout(s)

	if !view {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		n, cerr := docsession.RetryRead(cctx, nil, func(ctx context.Context) (int64, error) { return coll.EstimatedDocumentCount(ctx) })
		cancel()
		if cerr == nil {
			j.update(i, func(it *TransferItem) { it.Total = n })
		}
	}

	path := filepath.Join(req.Dir, j.Items[i].File)
	tmp := path + ".part"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if file != nil {
			_ = file.Close()
		}
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	cw := &docdump.CountingWriter{W: file}
	var out io.Writer = cw
	var zw *gzip.Writer
	if req.Gzip {
		zw = gzip.NewWriter(cw)
		out = zw
	}
	enc := docdump.NewEncoder(out, docdump.Format(req.Format), req.Canonical)

	var written int64
	report := func() {
		n, b := written, cw.N
		j.update(i, func(it *TransferItem) {
			it.Docs, it.Bytes = n, b
			if it.Total >= 0 && n > it.Total {
				it.Total = n
			}
		})
	}
	// Documents are read in _id order so that after a network failure the export can
	// resume where it stopped (skipping what was written) instead of starting over.
	err = withRetry(ctx, j, i, func(int) string { return fmt.Sprintf("Connection lost, resuming at document %d", written) }, func(ctx context.Context) error {
		opts := options.Find().SetBatchSize(exportBatchSize).SetSkip(written)
		if !view {
			opts.SetSort(bson.D{{Key: "_id", Value: 1}})
		}
		cctx, cancel := context.WithTimeout(ctx, timeout)
		cur, err := coll.Find(cctx, bson.D{}, opts)
		cancel()
		if err != nil {
			return err
		}
		defer func() {
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_ = cur.Close(cctx)
			cancel()
		}()
		for {
			var ok bool
			if cur.RemainingBatchLength() == 0 {
				// Fetching the next batch talks to the server: bound it so a dead link fails
				// fast (a batch normally takes milliseconds) and the export resumes.
				cctx, cancel := context.WithTimeout(ctx, min(timeout, batchTimeout))
				ok = cur.Next(cctx)
				cancel()
				report()
			} else {
				ok = cur.Next(ctx)
			}
			if !ok {
				return cur.Err()
			}
			if err := enc.Encode(cur.Current); err != nil {
				return fmt.Errorf("write %s: %w", path, err)
			}
			written++
			if exportFault != nil {
				if err := exportFault(written); err != nil {
					return err
				}
			}
		}
	})
	if err != nil {
		return err
	}
	if err = enc.Close(); err != nil {
		return err
	}
	if zw != nil {
		if err = zw.Close(); err != nil {
			return err
		}
	}
	if err = file.Close(); err != nil {
		return err
	}
	file = nil
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	report()

	if req.Indexes && !view {
		n, merr := writeMetadata(ctx, s, j.Database, name, filepath.Join(req.Dir, docdump.MetadataName(name)))
		if merr != nil {
			j.update(i, func(it *TransferItem) { it.Warning = "Indexes not saved: " + merr.Error() })
		} else {
			j.update(i, func(it *TransferItem) { it.Info = fmt.Sprintf("%d %s saved", n, plural(n, "index", "indexes")) })
		}
	}
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// writeMetadata saves the indexes and options of a collection, returning the number of indexes.
func writeMetadata(ctx context.Context, s *docsession.Session, db, name, path string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout(s))
	defer cancel()
	m := docdump.Metadata{CollectionName: name, Type: "collection"}
	idx, err := docsession.RetryRead(ctx, nil, func(ctx context.Context) ([]bson.D, error) {
		cur, err := s.Client.Database(db).Collection(name).Indexes().List(ctx)
		if err != nil {
			return nil, err
		}
		var specs []bson.D
		return specs, cur.All(ctx, &specs)
	})
	if err != nil {
		return 0, err
	}
	m.Indexes = idx
	if specs, err := s.Client.Database(db).ListCollectionSpecifications(ctx, bson.D{{Key: "name", Value: name}}); err == nil && len(specs) == 1 {
		var opts bson.D
		if bson.Unmarshal(specs[0].Options, &opts) == nil {
			m.Options = opts
		}
	}
	data, err := docdump.EncodeMetadata(m)
	if err != nil {
		return 0, err
	}
	return len(idx), os.WriteFile(path, data, 0o600)
}

func importFile(ctx context.Context, s *docsession.Session, j *job, i int) error {
	req := j.imp
	f := req.Files[i]
	coll := s.Client.Database(j.Database).Collection(f.Collection)
	timeout := opTimeout(s)

	if req.Mode == ImportReplace {
		err := withRetry(ctx, j, i, func(int) string { return "Connection lost while dropping the collection" }, func(ctx context.Context) error {
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			return coll.Drop(cctx)
		})
		if err != nil {
			return fmt.Errorf("drop %s: %w", f.Collection, err)
		}
	}

	file, err := os.Open(f.Path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if info, err := file.Stat(); err == nil {
		j.update(i, func(it *TransferItem) { it.TotalBytes = info.Size() })
	}
	cr := &docdump.CountingReader{R: file}
	dec, err := docdump.NewDecoder(cr, docdump.Format(f.Format))
	if err != nil {
		return fmt.Errorf("%s: %w", f.Name, err)
	}

	var docs, skipped, failed int64
	var firstErr string
	batch := make([]bson.Raw, 0, importBatchDocs)
	batchBytes := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := withRetry(ctx, j, i, func(int) string { return fmt.Sprintf("Connection lost, retrying the batch after document %d", docs) }, func(ctx context.Context) error {
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			return writeBatch(cctx, coll, req.Mode, batch)
		})
		var bwe mongo.BulkWriteException
		if errors.As(err, &bwe) && len(bwe.WriteErrors) > 0 && bwe.WriteConcernError == nil {
			for _, we := range bwe.WriteErrors {
				// A retried batch meets the documents it already inserted: those are fine.
				if we.Code == 11000 && req.Mode != ImportUpsert {
					skipped++
					continue
				}
				failed++
				if firstErr == "" {
					firstErr = we.Message
				}
			}
			err = nil
			if failed > 0 && req.StopOnError {
				err = fmt.Errorf("document rejected: %s", firstErr)
			}
		}
		if err != nil {
			return err
		}
		docs += int64(len(batch))
		batch, batchBytes = batch[:0], 0
		n, sk, fl, b := docs, skipped, failed, cr.N
		j.update(i, func(it *TransferItem) { it.Docs, it.Skipped, it.Failed, it.Bytes = n, sk, fl, b })
		return nil
	}
	for {
		doc, err := dec.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if ferr := flush(); ferr != nil {
				return ferr
			}
			return fmt.Errorf("%s: %w", f.Name, err)
		}
		batch = append(batch, doc)
		batchBytes += len(doc)
		if len(batch) >= importBatchDocs || batchBytes >= importBatchBytes {
			if err := flush(); err != nil {
				return err
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if err := flush(); err != nil {
		return err
	}
	j.update(i, func(it *TransferItem) {
		it.Bytes = it.TotalBytes
		it.Total = it.Docs
		if failed > 0 {
			it.Warning = fmt.Sprintf("%d documents rejected, e.g. %s", failed, firstErr)
		}
	})

	if req.Indexes && f.Metadata != "" {
		if n, err := restoreIndexes(ctx, s, j.Database, f.Collection, f.Metadata); err != nil {
			j.update(i, func(it *TransferItem) { it.Warning = joinWarning(it.Warning, "Indexes not restored: "+err.Error()) })
		} else if n > 0 {
			j.update(i, func(it *TransferItem) { it.Info = fmt.Sprintf("%d %s restored", n, plural(n, "index", "indexes")) })
		}
	}
	return nil
}

func joinWarning(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "; " + b
}

func writeBatch(ctx context.Context, coll *mongo.Collection, mode string, batch []bson.Raw) error {
	if mode != ImportUpsert {
		_, err := coll.InsertMany(ctx, batch, options.InsertMany().SetOrdered(false))
		return err
	}
	models := make([]mongo.WriteModel, len(batch))
	for k, d := range batch {
		if id, err := d.LookupErr("_id"); err == nil {
			models[k] = mongo.NewReplaceOneModel().SetFilter(bson.D{{Key: "_id", Value: id}}).SetReplacement(d).SetUpsert(true)
		} else {
			models[k] = mongo.NewInsertOneModel().SetDocument(d)
		}
	}
	_, err := coll.BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false))
	return err
}

func restoreIndexes(ctx context.Context, s *docsession.Session, db, coll, metaPath string) (int, error) {
	f, err := os.Open(metaPath)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	m, err := docdump.DecodeMetadata(f)
	if err != nil {
		return 0, err
	}
	var specs bson.A
	for _, ix := range m.Indexes {
		if n, _ := find(ix, "name"); n == "_id_" {
			continue
		}
		specs = append(specs, creationOptions(ix))
	}
	if len(specs) == 0 {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(ctx, max(opTimeout(s), 10*time.Minute))
	defer cancel()
	cmd := bson.D{{Key: "createIndexes", Value: coll}, {Key: "indexes", Value: specs}}
	return len(specs), s.Client.Database(db).RunCommand(ctx, cmd).Err()
}

// CollectionStats returns the document count and size of each collection in db, best
// effort: unknown values are -1.
func (t *Transfers) CollectionStats(ctx context.Context, sessionID, db string) ([]CollectionStat, error) {
	s, err := t.sessions.Get(sessionID)
	if err != nil {
		return nil, err
	}
	ctx, done := s.Begin(ctx, "")
	defer done()
	colls, err := docsession.RetryRead(ctx, nil, func(ctx context.Context) ([]docsession.Collection, error) { return s.ListCollections(ctx, db) })
	if err != nil {
		return nil, err
	}
	colls = slices.DeleteFunc(colls, func(c docsession.Collection) bool { return strings.HasPrefix(c.Name, "system.") })
	out := make([]CollectionStat, len(colls))
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for k, c := range colls {
		out[k] = CollectionStat{Name: c.Name, Type: c.Type, Count: -1, Size: -1}
		if c.Type == "view" {
			continue
		}
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			var st struct {
				Count int64 `bson:"count"`
				Size  int64 `bson:"size"`
			}
			if s.Client.Database(db).RunCommand(cctx, bson.D{{Key: "collStats", Value: c.Name}}).Decode(&st) == nil {
				out[k].Count, out[k].Size = st.Count, st.Size
			} else if n, err := s.Client.Database(db).Collection(c.Name).EstimatedDocumentCount(cctx); err == nil {
				out[k].Count = n
			}
		})
	}
	wg.Wait()
	return out, nil
}

// ScanImportFiles inspects picked files and folders (one level deep) for dumps.
func (t *Transfers) ScanImportFiles(paths []string) ImportScan {
	var scan ImportScan
	seen := map[string]bool{}
	add := func(p string, explicit bool) {
		if seen[p] {
			return
		}
		seen[p] = true
		f, ok := inspectImportFile(p)
		if ok {
			scan.Files = append(scan.Files, f)
		} else if explicit && !strings.HasSuffix(strings.ToLower(strings.TrimSuffix(p, ".gz")), ".metadata.json") {
			scan.Skipped = append(scan.Skipped, filepath.Base(p))
		}
	}
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			scan.Skipped = append(scan.Skipped, filepath.Base(p))
			continue
		}
		if !info.IsDir() {
			add(p, true)
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				add(filepath.Join(p, e.Name()), false)
			}
		}
	}
	slices.SortStableFunc(scan.Files, func(a, b ImportFile) int { return strings.Compare(a.Collection, b.Collection) })
	return scan
}

func inspectImportFile(p string) (ImportFile, bool) {
	format, gz, stem, ok := docdump.Detect(p)
	if !ok {
		return ImportFile{}, false
	}
	info, err := os.Stat(p)
	if err != nil {
		return ImportFile{}, false
	}
	f := ImportFile{Path: p, Name: filepath.Base(p), Size: info.Size(), Format: string(format), Gzip: gz, Collection: stem}
	dir := filepath.Dir(p)
	for _, name := range []string{stem + ".metadata.json", stem + ".metadata.json.gz"} {
		mp := filepath.Join(dir, name)
		r, err := os.Open(mp)
		if err != nil {
			continue
		}
		m, err := docdump.DecodeMetadata(r)
		_ = r.Close()
		if err != nil {
			continue
		}
		f.Metadata = mp
		if m.CollectionName != "" {
			f.Collection = m.CollectionName
		}
		for _, ix := range m.Indexes {
			if n, _ := find(ix, "name"); n != "_id_" {
				f.Indexes++
			}
		}
		break
	}
	return f, true
}

// PickExportDir asks for the folder to export into. "" means cancelled.
func (t *Transfers) PickExportDir() (string, error) {
	return application.Get().Dialog.OpenFile().
		SetTitle("Export to folder").
		SetButtonText("Export here").
		CanChooseFiles(false).
		CanChooseDirectories(true).
		CanCreateDirectories(true).
		PromptForSingleSelection()
}

// PickImportFiles asks for dump files or folders and inspects them.
func (t *Transfers) PickImportFiles() (ImportScan, error) {
	paths, err := application.Get().Dialog.OpenFile().
		SetTitle("Import files").
		SetButtonText("Add").
		CanChooseFiles(true).
		CanChooseDirectories(true).
		PromptForMultipleSelection()
	if err != nil || len(paths) == 0 {
		return ImportScan{}, err
	}
	return t.ScanImportFiles(paths), nil
}

// Reveal shows a file or folder in the system file manager.
func (t *Transfers) Reveal(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return application.Get().Env.OpenFileManager(path, !info.IsDir())
}
