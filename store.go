package dqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pkg/errors"

	"github.com/CanonicalLtd/dqlite/internal/client"
	_ "github.com/mattn/go-sqlite3" // Go SQLite bindings
)

// ServerStore is used by a dqlite client to get an initial list of candidate
// dqlite server addresses that it can dial in order to find a leader dqlite
// server to use.
//
// Once connected, the client periodically updates the addresses in the store
// by querying the leader server about changes in the cluster (such as servers
// being added or removed).
type ServerStore = client.ServerStore

// InmemServerStore keeps the list of target gRPC SQL servers in memory.
type InmemServerStore = client.InmemServerStore

// NewInmemServerStore creates ServerStore which stores its data in-memory.
var NewInmemServerStore = client.NewInmemServerStore

// DatabaseServerStore persists a list addresses of dqlite servers in a SQL table.
type DatabaseServerStore struct {
	db     *sql.DB // Database handle to use.
	schema string  // Name of the schema holding the servers table.
	table  string  // Name of the servers table.
	column string  // Column name in the servers table holding the server address.
}

// DefaultServerStore creates a new ServerStore using the given filename to
// open a SQLite database, with default names for the schema, table and column
// parameters.
//
// It also creates the table if it doesn't exist yet.
func DefaultServerStore(filename string) (*DatabaseServerStore, error) {
	// Open the database.
	db, err := sql.Open("sqlite3", filename)
	if err != nil {
		return nil, errors.Wrap(err, "failed to open database")
	}

	// Since we're setting SQLite single-thread mode, we need to have one
	// connection at most.
	db.SetMaxOpenConns(1)

	// Create the servers table if it does not exist yet.
	_, err = db.Exec("CREATE TABLE IF NOT EXISTS servers (address TEXT, UNIQUE(address))")
	if err != nil {
		return nil, errors.Wrap(err, "failed to create servers table")
	}

	store := NewServerStore(db, "main", "servers", "address")

	return store, nil
}

// NewServerStore creates a new ServerStore.
func NewServerStore(db *sql.DB, schema, table, column string) *DatabaseServerStore {
	return &DatabaseServerStore{
		db:     db,
		schema: schema,
		table:  table,
		column: column,
	}
}

// Get the current servers.
func (d *DatabaseServerStore) Get(ctx context.Context) ([]string, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return nil, errors.Wrap(err, "failed to begin transaction")
	}
	defer tx.Rollback()

	query := fmt.Sprintf("SELECT %s FROM %s.%s", d.column, d.schema, d.table)
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, errors.Wrap(err, "failed to query servers table")
	}
	defer rows.Close()

	addresses := make([]string, 0)
	for rows.Next() {
		var address string
		err := rows.Scan(&address)
		if err != nil {
			return nil, errors.Wrap(err, "failed to fetch server address")
		}
		addresses = append(addresses, address)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "result set failure")
	}

	return addresses, nil
}

// Set the servers addresses.
func (d *DatabaseServerStore) Set(ctx context.Context, addresses []string) error {
	tx, err := d.db.Begin()
	if err != nil {
		return errors.Wrap(err, "failed to begin transaction")
	}

	query := fmt.Sprintf("DELETE FROM %s.%s", d.schema, d.table)
	if _, err := tx.ExecContext(ctx, query); err != nil {
		tx.Rollback()
		return errors.Wrap(err, "failed to delete existing servers rows")
	}

	query = fmt.Sprintf("INSERT INTO %s.%s(%s) VALUES (?)", d.schema, d.table, d.column)
	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		tx.Rollback()
		return errors.Wrap(err, "failed to prepare insert statement")
	}
	defer stmt.Close()

	for _, address := range addresses {
		if _, err := stmt.ExecContext(ctx, address); err != nil {
			tx.Rollback()
			return errors.Wrapf(err, "failed to insert server %s", address)
		}
	}

	if err := tx.Commit(); err != nil {
		return errors.Wrap(err, "failed to commit transaction")
	}

	return nil
}