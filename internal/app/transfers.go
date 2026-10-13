package app

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
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

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/query"
	"github.com/iagapie/quire/internal/session"
	"github.com/iagapie/quire/internal/sqlsplit"
	"github.com/iagapie/quire/internal/transfer"
)

// Events of the transfer service.
const (
	// EventTransfer carries a TransferJob snapshot whenever an export or import progresses.
	EventTransfer = "transfer:progress"
	// EventFilesDropped carries the dumps dropped onto an import tab.
	EventFilesDropped = "import:dropped"
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
	ImportInsert  = "insert"  // add the rows; rows the server rejects (duplicates) are counted
	ImportUpsert  = "upsert"  // replace rows with the same primary key, insert the rest
	ImportReplace = "replace" // delete the rows of the table first
)

// ExportRequest describes an export of tables to a directory, one file each.
type ExportRequest struct {
	SessionID string   `json:"sessionId"`
	Database  string   `json:"database"`
	Schema    string   `json:"schema"`
	Tables    []string `json:"tables"`
	Dir       string   `json:"dir"`
	// Format is "csv", "jsonl", "json" or "sql".
	Format string `json:"format"`
	Gzip   bool   `json:"gzip"`
	// Definitions saves each table's CREATE statements: at the top of SQL dumps, in a
	// <table>.schema.sql file next to the others.
	Definitions bool `json:"definitions"`
}

// ImportFile is a dump file and the table it goes into.
type ImportFile struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Format string `json:"format"`
	Gzip   bool   `json:"gzip"`
	Table  string `json:"table"`
	// Definition is the path of a matching <table>.schema.sql, if any.
	Definition string `json:"definition"`
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
	Schema    string       `json:"schema"`
	Files     []ImportFile `json:"files"`
	Mode      string       `json:"mode"`
	// Create runs the table definition files of tables that do not exist.
	Create      bool `json:"create"`
	StopOnError bool `json:"stopOnError"`
}

// TransferItem is the progress of one table.
type TransferItem struct {
	Table string `json:"table"`
	File  string `json:"file"`
	State string `json:"state"`
	Rows  int64  `json:"rows"`
	// Total is the expected number of rows, or -1 if unknown.
	Total      int64 `json:"total"`
	Bytes      int64 `json:"bytes"`
	TotalBytes int64 `json:"totalBytes"`
	// Failed counts rows the server rejected.
	Failed  int64  `json:"failed"`
	Error   string `json:"error"`
	Warning string `json:"warning"`
	// Info is a note about extra work done, such as a created table.
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
	Schema    string         `json:"schema"`
	Dir       string         `json:"dir"`
	Format    string         `json:"format"`
	Mode      string         `json:"mode"`
	State     string         `json:"state"`
	StartedAt string         `json:"startedAt"`
	ElapsedMs int64          `json:"elapsedMs"`
	Items     []TransferItem `json:"items"`
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

// Transfers exports and imports tables in the background, reporting progress with
// EventTransfer.
type Transfers struct {
	conns *Connections

	mu   sync.Mutex
	jobs map[string]*job
	seq  int
}

// NewTransfers creates the service.
func NewTransfers(conns *Connections) *Transfers {
	return &Transfers{conns: conns, jobs: map[string]*job{}}
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
	importBatchRows    = 500
	copyBatchRows      = 5000
	defaultMaxParams   = 10000
	progressInterval   = 250 * time.Millisecond
	// readIdle bounds the wait for the next row of an export: a dead link fails and
	// the export resumes.
	readIdle = 60 * time.Second
	// exportFault, when set by tests, can fail the export after a row is written.
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

// Job returns one transfer.
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

// Cancel stops a running transfer. Finished tables are kept.
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

// Remove forgets a finished transfer.
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

// Retry runs the failed and cancelled tables of a finished transfer again.
func (t *Transfers) Retry(id string) (TransferJob, error) {
	j, err := t.job(id)
	if err != nil {
		return TransferJob{}, err
	}
	s, err := t.conns.get(j.SessionID)
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
			*it = TransferItem{Table: it.Table, File: it.File, State: statePending, Total: -1, TotalBytes: it.TotalBytes}
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
	s, err := t.conns.get(req.SessionID)
	if err != nil {
		return TransferJob{}, err
	}
	f := transfer.Format(req.Format)
	switch {
	case !f.Valid():
		return TransferJob{}, fmt.Errorf("unknown format %q", req.Format)
	case f == transfer.SQL && s.Info.Document:
		return TransferJob{}, errors.New("documents are exported as JSON or CSV, not SQL")
	case len(req.Tables) == 0:
		return TransferJob{}, errors.New("choose at least one table")
	case req.Dir == "":
		return TransferJob{}, errors.New("choose a destination folder")
	}
	if err := os.MkdirAll(req.Dir, 0o750); err != nil {
		return TransferJob{}, fmt.Errorf("destination folder: %w", err)
	}
	if err := checkWritable(req.Dir); err != nil {
		return TransferJob{}, err
	}
	j := t.newJob("export", req.SessionID, req.Database, req.Schema)
	j.export = &req
	j.Dir, j.Format = req.Dir, req.Format
	for _, name := range req.Tables {
		j.Items = append(j.Items, TransferItem{Table: name, File: transfer.FileName(name, f, req.Gzip), State: statePending, Total: -1})
	}
	t.start(s, j, nil)
	return j.snapshot(), nil
}

// StartImport validates an import and starts it in the background.
func (t *Transfers) StartImport(req ImportRequest) (TransferJob, error) {
	s, err := t.conns.get(req.SessionID)
	if err != nil {
		return TransferJob{}, err
	}
	if s.Profile.ReadOnly || s.Info.QueryOnly {
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
		req.Files[i].Table = strings.TrimSpace(f.Table)
		if req.Files[i].Table == "" && transfer.Format(f.Format) != transfer.SQL {
			return TransferJob{}, fmt.Errorf("%s: choose a target table", f.Name)
		}
		if !transfer.Format(f.Format).Valid() {
			return TransferJob{}, fmt.Errorf("%s: unsupported file", f.Name)
		}
	}
	j := t.newJob("import", req.SessionID, req.Database, req.Schema)
	j.imp = &req
	j.Mode = req.Mode
	for _, f := range req.Files {
		j.Items = append(j.Items, TransferItem{Table: f.Table, File: f.Name, State: statePending, Total: -1, TotalBytes: f.Size})
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

func (t *Transfers) newJob(kind, sessionID, db, schema string) *job {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	now := time.Now()
	j := &job{TransferJob: TransferJob{
		ID:        fmt.Sprintf("%s-%s-%03d", kind, now.Format("20060102T150405"), t.seq),
		Kind:      kind,
		SessionID: sessionID,
		Database:  db,
		Schema:    schema,
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
func (t *Transfers) start(s *session.Session, j *job, items []int) {
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
		// Files going into the same table must not race (a replace would wipe the other).
		seen := map[string]bool{}
		for _, f := range j.imp.Files {
			if seen[f.Table] || f.Table == "" {
				workers = 1
			}
			seen[f.Table] = true
		}
	}
	workers = min(workers, len(items))

	go func() {
		defer close(done)
		defer cancel()
		stop := make(chan struct{})
		go j.emitLoop(stop)

		var stats map[string]int64
		if j.export != nil {
			stats = tableRows(ctx, s, j.Database, j.Schema)
		}
		queue := make(chan int)
		var wg sync.WaitGroup
		for range workers {
			wg.Go(func() {
				for i := range queue {
					runItem(ctx, s, j, i, stats)
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

func runItem(ctx context.Context, s *session.Session, j *job, i int, stats map[string]int64) {
	if ctx.Err() != nil {
		return
	}
	j.update(i, func(it *TransferItem) {
		it.State = stateRunning
		it.started = time.Now()
		if n, ok := stats[it.Table]; ok && n >= 0 {
			it.Total = n
		}
	})
	var err error
	if j.export != nil {
		err = exportTable(ctx, s, j, i)
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

// tableRows estimates the rows of each table, for progress.
func tableRows(ctx context.Context, s *session.Session, database, schema string) map[string]int64 {
	out := map[string]int64{}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stats, err := session.RetryRead(cctx, s, nil, func(ctx context.Context) ([]dialect.TableStat, error) {
		db, err := s.DB(ctx, database)
		if err != nil {
			return nil, err
		}
		return s.Driver.TableStats(ctx, db, database, schema)
	})
	if err == nil {
		for _, st := range stats {
			out[st.Name] = st.Rows
		}
	}
	return out
}

func opTimeout(s *session.Session) time.Duration {
	if d := s.Timeout(); d > 0 {
		return d
	}
	return 10 * time.Minute
}

// retryable reports whether a transfer step should be retried after err: network
// failures, and per-operation timeouts while the transfer itself is still wanted.
func retryable(ctx context.Context, s *session.Session, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	return s.Driver.Transient(err) || errors.Is(err, context.DeadlineExceeded)
}

// withRetry runs fn until it succeeds, fails permanently or runs out of attempts,
// reporting retries on the item.
func withRetry(ctx context.Context, s *session.Session, j *job, i int, what func() string, fn func(context.Context) error) error {
	for attempt := 1; ; attempt++ {
		err := fn(ctx)
		if err == nil {
			if attempt > 1 {
				j.update(i, func(it *TransferItem) { it.Retry = "" })
			}
			return nil
		}
		if !retryable(ctx, s, err) || attempt > transferMaxRetries {
			return err
		}
		s.Failed(err)
		msg := fmt.Sprintf("%s (attempt %d of %d): %v", what(), attempt, transferMaxRetries, err)
		j.update(i, func(it *TransferItem) { it.Retry = msg })
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(min(time.Duration(attempt)*transferBackoff, 15*time.Second)):
		}
	}
}

// tableColumns reads the columns of a table, with retries.
func tableColumns(ctx context.Context, s *session.Session, database, schema, table string) ([]dialect.Column, error) {
	cctx, cancel := context.WithTimeout(ctx, opTimeout(s))
	defer cancel()
	return session.RetryRead(cctx, s, nil, func(ctx context.Context) ([]dialect.Column, error) {
		db, err := s.DB(ctx, database)
		if err != nil {
			return nil, err
		}
		return s.Driver.Columns(ctx, db, database, schema, table)
	})
}

// exportTable writes the rows of a table to its file. Rows are read in primary key
// order, so after a network failure the export resumes where it stopped (skipping
// what was written) instead of starting over.
func exportTable(ctx context.Context, s *session.Session, j *job, i int) (err error) {
	req := j.export
	name := j.Items[i].Table
	drv := s.Driver
	format := transfer.Format(req.Format)
	table := drv.Qualify(j.Database, j.Schema, name)

	cols, err := tableColumns(ctx, s, j.Database, j.Schema, name)
	if err != nil {
		return err
	}
	var key []string
	for _, c := range sortByPK(cols) {
		key = append(key, c.Name)
	}
	stmt := "SELECT * FROM " + table
	if len(key) > 0 && !s.Info.QueryOnly {
		q := make([]string, len(key))
		for k, c := range key {
			q[k] = drv.Quote(c)
		}
		stmt += " ORDER BY " + strings.Join(q, ", ")
	}

	definition := ""
	if req.Definitions && !s.Info.Document {
		ddl, derr := tableDDL(ctx, s, j.Database, j.Schema, name)
		if derr != nil {
			j.update(i, func(it *TransferItem) { it.Warning = "Table definition not saved: " + derr.Error() })
		} else {
			definition = ddl
			if format != transfer.SQL {
				if werr := os.WriteFile(filepath.Join(req.Dir, transfer.SchemaName(name)), []byte(ddl), 0o600); werr != nil {
					j.update(i, func(it *TransferItem) { it.Warning = "Table definition not saved: " + werr.Error() })
				} else {
					j.update(i, func(it *TransferItem) { it.Info = "Table definition saved" })
				}
			}
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
	cw := &transfer.CountingWriter{W: file}
	var out io.Writer = cw
	var zw *gzip.Writer
	if req.Gzip {
		zw = gzip.NewWriter(cw)
		out = zw
	}

	var (
		enc     *transfer.Encoder
		written int64
		kinds   []string
	)
	report := func() {
		n, b := written, cw.N
		j.update(i, func(it *TransferItem) {
			it.Rows, it.Bytes = n, b
			if it.Total >= 0 && n > it.Total {
				it.Total = n
			}
		})
	}
	err = withRetry(ctx, s, j, i, func() string { return fmt.Sprintf("Connection lost, resuming at row %d", written) }, func(ctx context.Context) error {
		db, err := s.DB(ctx, j.Database)
		if err != nil {
			return err
		}
		q, skip := stmt, written
		if written > 0 && len(key) > 0 {
			if lq := drv.Limit(stmt, 1<<53, written); lq != "" {
				q, skip = lq, 0
			}
		}
		qctx, cancel := context.WithCancel(ctx)
		defer cancel()
		idle := time.AfterFunc(max(opTimeout(s), readIdle), cancel)
		defer idle.Stop()
		rows, err := db.QueryContext(qctx, q)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		rcols, err := query.Columns(rows)
		if err != nil {
			return err
		}
		if enc == nil {
			tcols := make([]transfer.Column, len(rcols))
			kinds = make([]string, len(rcols))
			for k, c := range rcols {
				tcols[k] = transfer.Column{Name: c.Name, Kind: c.Kind}
				kinds[k] = c.Kind
			}
			names := make([]string, len(rcols))
			for k, c := range rcols {
				names[k] = drv.Quote(c.Name)
			}
			prefix := "INSERT INTO " + table + " (" + strings.Join(names, ", ") + ") VALUES ("
			insert := func(values []*string) string {
				lits := make([]string, len(values))
				for k, v := range values {
					lits[k] = dialect.Literal(drv, kinds[k], v)
				}
				return prefix + strings.Join(lits, ", ") + ")"
			}
			if enc, err = transfer.NewEncoder(out, format, tcols, definition, insert); err != nil {
				return err
			}
		}
		for rows.Next() {
			idle.Reset(max(opTimeout(s), readIdle))
			row, err := query.FullRow(rows, rcols, drv)
			if err != nil {
				return err
			}
			if skip > 0 {
				skip--
				continue
			}
			if err := enc.Encode(row); err != nil {
				return fmt.Errorf("write %s: %w", path, err)
			}
			written++
			if written%1000 == 0 {
				report()
			}
			if exportFault != nil {
				if err := exportFault(written); err != nil {
					return err
				}
			}
		}
		return rows.Err()
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
	return nil
}

func sortByPK(cols []dialect.Column) []dialect.Column {
	var key []dialect.Column
	for _, c := range cols {
		if c.PK > 0 {
			key = append(key, c)
		}
	}
	slices.SortFunc(key, func(a, b dialect.Column) int { return a.PK - b.PK })
	return key
}

func tableDDL(ctx context.Context, s *session.Session, database, schema, name string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, opTimeout(s))
	defer cancel()
	return session.RetryRead(cctx, s, nil, func(ctx context.Context) (string, error) {
		db, err := s.DB(ctx, database)
		if err != nil {
			return "", err
		}
		return s.Driver.DDL(ctx, db, database, schema, dialect.Object{Name: name, Kind: dialect.KindTable})
	})
}

// importFile loads one dump file: SQL scripts are run statement by statement, other
// formats are inserted into the target table in batches.
func importFile(ctx context.Context, s *session.Session, j *job, i int) error {
	req := j.imp
	f := req.Files[i]
	file, err := os.Open(f.Path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if info, err := file.Stat(); err == nil {
		j.update(i, func(it *TransferItem) { it.TotalBytes = info.Size() })
	}
	cr := &transfer.CountingReader{R: file}
	r, closeR, err := transfer.Reader(cr, f.Gzip)
	if err != nil {
		return fmt.Errorf("%s: %w", f.Name, err)
	}
	defer func() { _ = closeR() }()

	if transfer.Format(f.Format) == transfer.SQL {
		return runScript(ctx, s, j, i, r, cr)
	}
	return loadRows(ctx, s, j, i, r, cr)
}

// runScript runs the statements of an SQL dump in order, on one connection.
func runScript(ctx context.Context, s *session.Session, j *job, i int, r io.Reader, cr *transfer.CountingReader) error {
	req := j.imp
	text, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	stmts := sqlsplit.Split(string(text), s.Driver.Split())
	j.update(i, func(it *TransferItem) { it.Total = int64(len(stmts)) })
	db, err := s.DB(ctx, j.Database)
	if err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	for _, stmt := range s.Driver.Use(j.Database, j.Schema) {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	var done, failed int64
	firstErr := ""
	for _, st := range stmts {
		sctx, cancel := context.WithTimeout(ctx, opTimeout(s))
		_, err := conn.ExecContext(sctx, st.Text)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if s.Driver.Transient(err) {
				return fmt.Errorf("line %d: the connection was lost; %d statements ran: %w", st.Line, done, err)
			}
			failed++
			if firstErr == "" {
				firstErr = fmt.Sprintf("line %d: %v", st.Line, err)
			}
			if req.StopOnError {
				return errors.New(firstErr)
			}
		} else {
			done++
		}
		n, fl, b := done, failed, cr.N
		j.update(i, func(it *TransferItem) { it.Rows, it.Failed, it.Bytes = n, fl, b })
	}
	if failed > 0 {
		j.update(i, func(it *TransferItem) { it.Warning = fmt.Sprintf("%d statements failed, e.g. %s", failed, firstErr) })
	}
	j.update(i, func(it *TransferItem) { it.Bytes = it.TotalBytes })
	return nil
}

// target describes the table rows are loaded into.
type target struct {
	name  string // qualified
	cols  map[string]dialect.Column
	key   []string
	kinds map[string]string
}

func loadRows(ctx context.Context, s *session.Session, j *job, i int, r io.Reader, cr *transfer.CountingReader) error {
	req := j.imp
	f := req.Files[i]
	drv := s.Driver
	cols, err := tableColumns(ctx, s, j.Database, j.Schema, f.Table)
	if err != nil {
		return err
	}
	if len(cols) == 0 && req.Create && f.Definition != "" {
		if err := createTable(ctx, s, j, f.Definition); err != nil {
			return fmt.Errorf("create %s: %w", f.Table, err)
		}
		j.update(i, func(it *TransferItem) { it.Info = "Table created" })
		if cols, err = tableColumns(ctx, s, j.Database, j.Schema, f.Table); err != nil {
			return err
		}
	}
	if len(cols) == 0 {
		hint := ""
		if f.Definition != "" {
			hint = "; turn on \"Create missing tables\" to create it from " + filepath.Base(f.Definition)
		}
		return fmt.Errorf("table %s not found%s", f.Table, hint)
	}
	tg := target{name: drv.Qualify(j.Database, j.Schema, f.Table), cols: map[string]dialect.Column{}, kinds: map[string]string{}}
	for _, c := range cols {
		tg.cols[c.Name] = c
		tg.kinds[c.Name] = query.KindOf(c.Type)
	}
	for _, c := range sortByPK(cols) {
		tg.key = append(tg.key, c.Name)
	}
	if req.Mode == ImportUpsert && (len(tg.key) == 0 || drv.Upsert(tg.name, tg.key, tg.key) == "") {
		return fmt.Errorf("%s cannot be upserted: it has no primary key, or the database does not support it", f.Table)
	}
	db, err := s.DB(ctx, j.Database)
	if err != nil {
		return err
	}
	if req.Mode == ImportReplace {
		err := withRetry(ctx, s, j, i, func() string { return "Connection lost while deleting the rows" }, func(ctx context.Context) error {
			cctx, cancel := context.WithTimeout(ctx, opTimeout(s))
			defer cancel()
			_, err := db.ExecContext(cctx, dialect.DeleteAll(drv, tg.name))
			return err
		})
		if err != nil {
			return fmt.Errorf("delete the rows of %s: %w", f.Table, err)
		}
	}

	dec, err := transfer.NewDecoder(r, transfer.Format(f.Format))
	if err != nil {
		return fmt.Errorf("%s: %w", f.Name, err)
	}
	var (
		rows, failed int64
		firstErr     string
		ignored      = map[string]bool{}
		batch        []transfer.Record
	)
	limit := importBatchRows
	if _, ok := dialect.CopierOf(drv); ok && req.Mode != ImportUpsert {
		limit = copyBatchRows
	}
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		ok, bad, msg, err := writeBatch(ctx, s, j, i, db, tg, req.Mode, batch, rows)
		if err != nil {
			return err
		}
		rows += ok
		failed += bad
		if firstErr == "" {
			firstErr = msg
		}
		if bad > 0 && req.StopOnError {
			return fmt.Errorf("row rejected: %s", msg)
		}
		batch = batch[:0]
		n, fl, b := rows, failed, cr.N
		j.update(i, func(it *TransferItem) { it.Rows, it.Failed, it.Bytes = n, fl, b })
		return nil
	}
	for {
		rec, err := dec.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if ferr := flush(); ferr != nil {
				return ferr
			}
			return fmt.Errorf("%s: %w", f.Name, err)
		}
		// Only the table's columns are loaded.
		var keep transfer.Record
		for k, c := range rec.Columns {
			if _, ok := tg.cols[c]; ok {
				keep.Columns = append(keep.Columns, c)
				keep.Values = append(keep.Values, rec.Values[k])
			} else {
				ignored[c] = true
			}
		}
		if len(keep.Columns) == 0 {
			return fmt.Errorf("%s: no column of the file is a column of %s", f.Name, f.Table)
		}
		batch = append(batch, keep)
		if len(batch) >= limit {
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
		it.Total = it.Rows + it.Failed
		var warn []string
		if failed > 0 {
			warn = append(warn, fmt.Sprintf("%d rows rejected, e.g. %s", failed, firstErr))
		}
		if len(ignored) > 0 {
			names := make([]string, 0, len(ignored))
			for n := range ignored {
				names = append(names, n)
			}
			slices.Sort(names)
			warn = append(warn, "columns not in the table were ignored: "+strings.Join(names, ", "))
		}
		it.Warning = strings.Join(warn, "; ")
	})
	return nil
}

func createTable(ctx context.Context, s *session.Session, j *job, path string) error {
	text, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	db, err := s.DB(ctx, j.Database)
	if err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	for _, stmt := range s.Driver.Use(j.Database, j.Schema) {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	for _, st := range sqlsplit.Split(string(text), s.Driver.Split()) {
		if _, err := conn.ExecContext(ctx, st.Text); err != nil {
			return fmt.Errorf("line %d: %w", st.Line, err)
		}
	}
	return nil
}

// writeBatch loads a batch in one transaction (COPY, multi-row INSERT or upserts),
// retrying on network failures. When the server rejects it, the rows are written one
// by one so the good ones go in and the bad ones are counted. It returns the rows
// written, the rows rejected and the first rejection.
func writeBatch(ctx context.Context, s *session.Session, j *job, i int, db *sql.DB, tg target, mode string, batch []transfer.Record, before int64) (int64, int64, string, error) {
	var rejected error
	err := withRetry(ctx, s, j, i, func() string { return fmt.Sprintf("Connection lost, retrying the batch after row %d", before) }, func(ctx context.Context) error {
		cctx, cancel := context.WithTimeout(ctx, opTimeout(s))
		defer cancel()
		err := writeAll(cctx, s, db, tg, mode, batch)
		if err != nil && !s.Driver.Transient(err) && ctx.Err() == nil && !errors.Is(err, context.DeadlineExceeded) {
			rejected = err
			return nil
		}
		return err
	})
	if err != nil {
		return 0, 0, "", err
	}
	if rejected == nil {
		return int64(len(batch)), 0, "", nil
	}
	// Row by row: the server tells which rows it refuses.
	var ok, bad int64
	first := ""
	for _, rec := range batch {
		var rowErr error
		err := withRetry(ctx, s, j, i, func() string { return fmt.Sprintf("Connection lost, retrying row %d", before+ok+bad+1) }, func(ctx context.Context) error {
			cctx, cancel := context.WithTimeout(ctx, opTimeout(s))
			defer cancel()
			stmt, args := rowStatement(s.Driver, tg, mode, rec)
			_, err := db.ExecContext(cctx, stmt, args...)
			if err != nil && !s.Driver.Transient(err) && ctx.Err() == nil {
				rowErr = err
				return nil
			}
			return err
		})
		if err != nil {
			return ok, bad, first, err
		}
		if rowErr != nil {
			bad++
			if first == "" {
				first = rowErr.Error()
			}
			continue
		}
		ok++
	}
	return ok, bad, first, nil
}

// writeAll writes a batch in one transaction.
func writeAll(ctx context.Context, s *session.Session, db *sql.DB, tg target, mode string, batch []transfer.Record) error {
	drv := s.Driver
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if cp, ok := dialect.CopierOf(drv); ok && mode != ImportUpsert && sameColumns(batch) {
		var buf bytes.Buffer
		tcols := make([]transfer.Column, len(batch[0].Columns))
		for k, c := range batch[0].Columns {
			tcols[k] = transfer.Column{Name: c}
		}
		enc, err := transfer.NewEncoder(&buf, transfer.CSV, tcols, "", nil)
		if err != nil {
			return err
		}
		for _, rec := range batch {
			if err := enc.Encode(rec.Values); err != nil {
				return err
			}
		}
		if err := enc.Close(); err != nil {
			return err
		}
		_, err = cp.CopyCSV(ctx, conn, tg.name, batch[0].Columns, &buf)
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	maxParams := defaultMaxParams
	if bl, ok := drv.(dialect.BatchLimits); ok {
		maxParams = bl.MaxParams()
	}
	if mode == ImportUpsert || maxParams == 0 || !sameColumns(batch) {
		for _, rec := range batch {
			stmt, args := rowStatement(drv, tg, mode, rec)
			if _, err := tx.ExecContext(ctx, stmt, args...); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	cols := batch[0].Columns
	per := max(1, min(importBatchRows, maxParams/len(cols)))
	for start := 0; start < len(batch); start += per {
		chunk := batch[start:min(start+per, len(batch))]
		args := make([]any, 0, len(chunk)*len(cols))
		for _, rec := range chunk {
			args = append(args, params(drv, tg, rec)...)
		}
		if _, err := tx.ExecContext(ctx, dialect.InsertRows(drv, tg.name, cols, len(chunk)), args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func sameColumns(batch []transfer.Record) bool {
	for _, rec := range batch[1:] {
		if !slices.Equal(rec.Columns, batch[0].Columns) {
			return false
		}
	}
	return true
}

// rowStatement is the INSERT (or upsert) of one row.
func rowStatement(drv dialect.SQLDriver, tg target, mode string, rec transfer.Record) (string, []any) {
	if mode == ImportUpsert {
		return drv.Upsert(tg.name, rec.Columns, tg.key), params(drv, tg, rec)
	}
	return dialect.Insert(drv, tg.name, rec.Columns), params(drv, tg, rec)
}

// params converts text values to parameters: binary values to bytes, booleans to
// bool, and what the driver wants typed (Oracle dates).
func params(drv dialect.SQLDriver, tg target, rec transfer.Record) []any {
	out := make([]any, len(rec.Values))
	conv, _ := drv.(dialect.Params)
	for k, v := range rec.Values {
		if v == nil {
			continue
		}
		kind := tg.kinds[rec.Columns[k]]
		switch kind {
		case dialect.ValueBinary:
			if b, ok := dialect.HexBytes(*v); ok {
				out[k] = b
				continue
			}
		case dialect.ValueBool:
			if b, ok := dialect.ParseBool(*v); ok {
				out[k] = b
				continue
			}
		case dialect.ValueNumber:
			// JSON booleans going into numeric flag columns (MySQL TINYINT(1)).
			if b, ok := dialect.ParseBool(*v); ok && !dialect.IsNumber(*v) {
				out[k] = map[bool]int{true: 1, false: 0}[b]
				continue
			}
		}
		if conv != nil {
			if p, ok := conv.Param(kind, tg.cols[rec.Columns[k]].Type, *v); ok {
				out[k] = p
				continue
			}
		}
		out[k] = *v
	}
	return out
}

// TableStats returns the estimated rows and size of each table of a schema, for
// choosing what to export.
func (t *Transfers) TableStats(ctx context.Context, sessionID, database, schema string) ([]dialect.TableStat, error) {
	return catalog(ctx, t.conns, sessionID, database, func(ctx context.Context, s *session.Session, q dialect.Querier) ([]dialect.TableStat, error) {
		return s.Driver.TableStats(ctx, q, database, schema)
	})
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
		} else if explicit && !strings.HasSuffix(strings.ToLower(p), transfer.SchemaSuffix) {
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
	slices.SortStableFunc(scan.Files, func(a, b ImportFile) int { return strings.Compare(a.Table, b.Table) })
	return scan
}

func inspectImportFile(p string) (ImportFile, bool) {
	format, gz, stem, ok := transfer.Detect(p)
	if !ok {
		return ImportFile{}, false
	}
	info, err := os.Stat(p)
	if err != nil {
		return ImportFile{}, false
	}
	f := ImportFile{Path: p, Name: filepath.Base(p), Size: info.Size(), Format: string(format), Gzip: gz, Table: stem}
	if format == transfer.SQL {
		f.Table = ""
	}
	if def := filepath.Join(filepath.Dir(p), stem+transfer.SchemaSuffix); format != transfer.SQL {
		if _, err := os.Stat(def); err == nil {
			f.Definition = def
		}
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
