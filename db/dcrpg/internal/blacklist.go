// Copyright (c) 2019-2021, The Decred developers
// See LICENSE for details.

package internal

const (
	CreateBlackListTable = `
		CREATE TABLE IF NOT EXISTS black_list (
		ip TEXT,
		note TEXT,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY (ip)
	);`

	// UpsertIPRangeBlackList records (or refreshes) a blacklist entry. The
	// created_at timestamp is reset on conflict so the auto-expiry window in
	// CheckIPRangeExistOnBlackList restarts on repeated abuse.
	UpsertIPRangeBlackList = `
		INSERT INTO black_list (ip, note, created_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (ip) DO UPDATE
			SET note = EXCLUDED.note, created_at = NOW();`

	// CheckIPRangeExistOnBlackList only treats an entry as active for a limited
	// window so an auto-blacklisted client is not blocked permanently.
	CheckIPRangeExistOnBlackList = `SELECT EXISTS (
		SELECT 1 FROM black_list
		WHERE ip = $1 AND created_at > NOW() - INTERVAL '24 hours'
	);`
)
