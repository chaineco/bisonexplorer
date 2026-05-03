// Copyright (c) 2021, The Decred developers
// Copyright (c) 2017, The dcrdata developers
// See LICENSE for details.

package dcrpg

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/lib/pq" // Start the PostgreSQL driver
)

// Default connection pool limits. These are kept well below typical PostgreSQL
// max_connections (200) so that bursts of concurrent handlers cannot exhaust
// the server and trigger "pq: sorry, too many clients already".
const (
	defaultMaxOpenConns    = 80
	defaultMaxIdleConns    = 20
	defaultConnMaxLifetime = 30 * time.Minute
	defaultConnMaxIdleTime = 10 * time.Minute
)

// Connect opens a connection to a PostgreSQL database. The caller is
// responsible for calling Close() on the returned db when finished using it.
// The input host may be an IP address for TCP connection, or an absolute path
// to a UNIX domain socket. An empty string should be provided for UNIX sockets.
func Connect(host, port, user, pass, dbname string) (*sql.DB, error) {
	var psqlInfo string
	if pass == "" {
		psqlInfo = fmt.Sprintf("host=%s user=%s dbname=%s sslmode=disable",
			host, user, dbname)
	} else {
		psqlInfo = fmt.Sprintf("host=%s user=%s password=%s dbname=%s sslmode=disable",
			host, user, pass, dbname)
	}

	// Only add port arg for TCP connections since UNIX domain sockets
	// (specified by a "/" prefix) do not have a port.
	if !strings.HasPrefix(host, "/") {
		psqlInfo += fmt.Sprintf(" port=%s", port)
	}

	db, err := sql.Open("postgres", psqlInfo)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(defaultMaxOpenConns)
	db.SetMaxIdleConns(defaultMaxIdleConns)
	db.SetConnMaxLifetime(defaultConnMaxLifetime)
	db.SetConnMaxIdleTime(defaultConnMaxIdleTime)

	return db, db.Ping()
}
