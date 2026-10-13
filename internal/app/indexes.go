package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/session"
)

// IndexPlan is what saving an index runs.
type IndexPlan struct {
	Statements []string `json:"statements"`
	// Atomic tells the statements run in one transaction.
	Atomic bool `json:"atomic"`
	// Warning explains a risk (a rebuild that is not atomic, a lock).
	Warning string `json:"warning"`
}

// IndexOptions are the choices of the index editor that are not part of the index.
type IndexOptions struct {
	Concurrently bool `json:"concurrently"`
	IfNotExists  bool `json:"ifNotExists"`
}

// indexMinTimeout bounds index builds, which take longer than queries.
const indexMinTimeout = 30 * time.Minute

// PlanIndex returns the statements creating ix, or replacing the index original (drop
// then create) when original is set.
func (c *Connections) PlanIndex(sessionID, database, schema, original string, ix dialect.Index, opts IndexOptions) (IndexPlan, error) {
	s, err := c.get(sessionID)
	if err != nil {
		return IndexPlan{}, err
	}
	return planIndex(s, database, schema, original, ix, opts)
}

func planIndex(s *session.Session, database, schema, original string, ix dialect.Index, opts IndexOptions) (IndexPlan, error) {
	w, ok := s.Driver.(dialect.IndexWriter)
	if !ok || !s.Info.Index.Editable {
		return IndexPlan{}, errors.New("indexes cannot be changed on this database")
	}
	ix.Name = strings.TrimSpace(ix.Name)
	if ix.Table == "" {
		return IndexPlan{}, errors.New("no table")
	}
	var cols []dialect.IndexColumn
	for _, c := range ix.Columns {
		c.Name, c.Expression = strings.TrimSpace(c.Name), strings.TrimSpace(c.Expression)
		if c.Name != "" || c.Expression != "" {
			cols = append(cols, c)
		}
	}
	ix.Columns = cols
	create, err := w.CreateIndex(qualifierFor(s, database), schema, ix, opts.Concurrently && s.Info.Index.Concurrently, opts.IfNotExists && s.Info.Index.IfNotExists && original == "")
	if err != nil {
		return IndexPlan{}, err
	}
	plan := IndexPlan{Statements: []string{create}}
	if original != "" {
		plan.Statements = []string{w.DropIndex(qualifierFor(s, database), schema, ix.Table, original, opts.Concurrently && s.Info.Index.Concurrently), create}
		switch {
		case opts.Concurrently && s.Info.Index.Concurrently:
			plan.Warning = "Built concurrently, the index is dropped and created in two steps: queries run without it in between."
		case !s.Info.TransactionalDDL:
			plan.Warning = "This database cannot change indexes in a transaction: if creating the new index fails, the old one is already gone."
		}
	}
	concurrent := opts.Concurrently && s.Info.Index.Concurrently
	plan.Atomic = len(plan.Statements) > 1 && s.Info.TransactionalDDL && !concurrent
	return plan, nil
}

// qualifierFor is the database part of names for drivers whose connections reach
// several databases (MySQL).
func qualifierFor(s *session.Session, database string) string {
	if pd, ok := s.Driver.(dialect.PerDatabase); ok && pd.PerDatabasePools() {
		return ""
	}
	return database
}

// SaveIndex creates ix, or replaces the index original. opID makes the build
// cancellable with Queries.Cancel.
func (c *Connections) SaveIndex(ctx context.Context, sessionID, opID, database, schema, original string, ix dialect.Index, opts IndexOptions) (IndexPlan, error) {
	s, err := c.get(sessionID)
	if err != nil {
		return IndexPlan{}, err
	}
	if s.Profile.ReadOnly {
		return IndexPlan{}, errReadOnly
	}
	plan, err := planIndex(s, database, schema, original, ix, opts)
	if err != nil {
		return plan, err
	}
	return plan, runDDL(ctx, s, opID, database, plan.Statements, plan.Atomic)
}

// DropIndex drops an index.
func (c *Connections) DropIndex(ctx context.Context, sessionID, database, schema, table, name string, concurrently bool) error {
	s, err := c.get(sessionID)
	if err != nil {
		return err
	}
	if s.Profile.ReadOnly {
		return errReadOnly
	}
	w, ok := s.Driver.(dialect.IndexWriter)
	if !ok || !s.Info.Index.Editable {
		return errors.New("indexes cannot be changed on this database")
	}
	stmt := w.DropIndex(qualifierFor(s, database), schema, table, name, concurrently && s.Info.Index.Concurrently)
	return runDDL(ctx, s, "", database, []string{stmt}, false)
}

// DropIndexStatement returns the statement DropIndex runs, for the confirmation.
func (c *Connections) DropIndexStatement(sessionID, database, schema, table, name string) (string, error) {
	s, err := c.get(sessionID)
	if err != nil {
		return "", err
	}
	w, ok := s.Driver.(dialect.IndexWriter)
	if !ok {
		return "", errors.New("indexes cannot be changed on this database")
	}
	return w.DropIndex(qualifierFor(s, database), schema, table, name, false), nil
}

// runDDL runs statements on one connection of database, in a transaction when atomic.
// It is not retried: DDL is not idempotent.
func runDDL(ctx context.Context, s *session.Session, opID, database string, stmts []string, atomic bool) error {
	ctx, done := s.BeginWithTimeout(ctx, opID, max(s.Timeout(), indexMinTimeout))
	defer done()
	db, err := s.DB(ctx, database)
	if err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if !atomic {
		for _, st := range stmts {
			if _, err := conn.ExecContext(ctx, st); err != nil {
				return fmt.Errorf("%s: %w", firstLine(st), err)
			}
		}
		return nil
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, st := range stmts {
		if _, err := tx.ExecContext(ctx, st); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("%s: %w (nothing was changed)", firstLine(st), err)
		}
	}
	return tx.Commit()
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	if len(line) > 80 {
		line = line[:80] + "…"
	}
	return line
}
