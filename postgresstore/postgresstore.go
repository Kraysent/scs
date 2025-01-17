package postgresstore

import (
	"database/sql"
	"fmt"
	"log"
	"time"
)

// PostgresStore represents the session store.
type PostgresStore struct {
	db          *sql.DB
	stopCleanup chan bool
	opts        *storeOptions
}

var defaultOptions = storeOptions{
	sessionTableName: "sessions",
	tokenColumnName:  "token",
	dataColumnName:   "data",
	expiryColumnName: "expiry",
	cleanupInterval:  5 * time.Minute,
}

// New returns a new PostgresStore instance, with a background cleanup goroutine
// that runs every 5 minutes to remove expired session data.
func New(db *sql.DB, options ...StoreOption) *PostgresStore {
	storeOpts := defaultOptions

	for _, opt := range options {
		opt(&storeOpts)
	}

	p := &PostgresStore{
		db:   db,
		opts: &storeOpts,
	}

	if p.opts.cleanupInterval > 0 {
		go p.startCleanup(p.opts.cleanupInterval)
	}

	return p
}

// NewWithCleanupInterval returns a new PostgresStore instance. The cleanupInterval
// parameter controls how frequently expired session data is removed by the
// background cleanup goroutine. Setting it to 0 prevents the cleanup goroutine
// from running (i.e. expired sessions will not be removed).
func NewWithCleanupInterval(db *sql.DB, cleanupInterval time.Duration) *PostgresStore {
	return New(db, WithCleanupInterval(cleanupInterval))
}

// Find returns the data for a given session token from the PostgresStore instance.
// If the session token is not found or is expired, the returned exists flag will
// be set to false.
func (p *PostgresStore) Find(token string) (b []byte, exists bool, err error) {
	row := p.db.QueryRow(fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s = $1 AND current_timestamp < %s",
		p.opts.dataColumnName, p.opts.sessionTableName, p.opts.tokenColumnName, p.opts.expiryColumnName,
	), token)
	err = row.Scan(&b)
	if err == sql.ErrNoRows {
		return nil, false, nil
	} else if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

// Commit adds a session token and data to the PostgresStore instance with the
// given expiry time. If the session token already exists, then the data and expiry
// time are updated.
func (p *PostgresStore) Commit(token string, b []byte, expiry time.Time) error {
	_, err := p.db.Exec(fmt.Sprintf(
		"INSERT INTO %s (%s, %s, %s) VALUES ($1, $2, $3) ON CONFLICT (%s) DO UPDATE SET %s = EXCLUDED.%s, %s = EXCLUDED.%s",
		p.opts.sessionTableName, p.opts.tokenColumnName, p.opts.dataColumnName, p.opts.expiryColumnName,
		p.opts.tokenColumnName, p.opts.dataColumnName, p.opts.dataColumnName, p.opts.expiryColumnName,
		p.opts.expiryColumnName,
	), token, b, expiry)
	if err != nil {
		return err
	}
	return nil
}

// Delete removes a session token and corresponding data from the PostgresStore
// instance.
func (p *PostgresStore) Delete(token string) error {
	_, err := p.db.Exec(fmt.Sprintf(
		"DELETE FROM %s WHERE %s = $1",
		p.opts.sessionTableName, p.opts.tokenColumnName,
	), token)
	return err
}

// All returns a map containing the token and data for all active (i.e.
// not expired) sessions in the PostgresStore instance.
func (p *PostgresStore) All() (map[string][]byte, error) {
	rows, err := p.db.Query(fmt.Sprintf(
		"SELECT %s, %s FROM %s WHERE current_timestamp < %s",
		p.opts.tokenColumnName, p.opts.dataColumnName, p.opts.sessionTableName, p.opts.expiryColumnName,
	))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessions := make(map[string][]byte)

	for rows.Next() {
		var (
			token string
			data  []byte
		)

		err = rows.Scan(&token, &data)
		if err != nil {
			return nil, err
		}

		sessions[token] = data
	}

	err = rows.Err()
	if err != nil {
		return nil, err
	}

	return sessions, nil
}

func (p *PostgresStore) startCleanup(interval time.Duration) {
	p.stopCleanup = make(chan bool)
	ticker := time.NewTicker(interval)
	for {
		select {
		case <-ticker.C:
			err := p.deleteExpired()
			if err != nil {
				log.Println(err)
			}
		case <-p.stopCleanup:
			ticker.Stop()
			return
		}
	}
}

// StopCleanup terminates the background cleanup goroutine for the PostgresStore
// instance. It's rare to terminate this; generally PostgresStore instances and
// their cleanup goroutines are intended to be long-lived and run for the lifetime
// of your application.
//
// There may be occasions though when your use of the PostgresStore is transient.
// An example is creating a new PostgresStore instance in a test function. In this
// scenario, the cleanup goroutine (which will run forever) will prevent the
// PostgresStore object from being garbage collected even after the test function
// has finished. You can prevent this by manually calling StopCleanup.
func (p *PostgresStore) StopCleanup() {
	if p.stopCleanup != nil {
		p.stopCleanup <- true
	}
}

func (p *PostgresStore) deleteExpired() error {
	_, err := p.db.Exec(fmt.Sprintf(
		"DELETE FROM %s WHERE %s < current_timestamp",
		p.opts.sessionTableName, p.opts.expiryColumnName,
	))
	return err
}
