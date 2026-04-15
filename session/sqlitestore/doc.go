// Package sqlitestore provides a SQLite-backed implementation of session.Store.
//
// The package depends only on database/sql at runtime. Applications choose and
// import the SQLite driver they want to use, then pass an opened *sql.DB to New.
package sqlitestore
