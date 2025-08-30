// Copyright (c) 2016, 2018 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/decred/dcrd/rpcclient/v8"
	"github.com/decred/slog"
	"github.com/jrick/logrotate/rotator"

	"github.com/decred/dcrdata/cmd/dcrdata/internal/api"
	"github.com/decred/dcrdata/cmd/dcrdata/internal/api/insight"
	"github.com/decred/dcrdata/cmd/dcrdata/internal/explorer"
	"github.com/decred/dcrdata/cmd/dcrdata/internal/middleware"
	notify "github.com/decred/dcrdata/cmd/dcrdata/internal/notification"

	"github.com/decred/dcrdata/db/dcrpg/v8"
	"github.com/decred/dcrdata/exchanges/v3"
	"github.com/decred/dcrdata/gov/v6/agendas"
	"github.com/decred/dcrdata/gov/v6/politeia"

	"github.com/decred/dcrdata/v8/blockdata"
	"github.com/decred/dcrdata/v8/mempool"
	"github.com/decred/dcrdata/v8/pubsub"
	"github.com/decred/dcrdata/v8/rpcutils"
	"github.com/decred/dcrdata/v8/stakedb"
)

var (
	infoRotator  *rotator.Rotator
	debugRotator *rotator.Rotator
	backendLog   *splitBackend

	// subsystem loggers (initialized in initLogRotators)
	notifyLog     slog.Logger
	postgresqlLog slog.Logger
	stakedbLog    slog.Logger
	BlockdataLog  slog.Logger
	clientLog     slog.Logger
	mempoolLog    slog.Logger
	expLog        slog.Logger
	apiLog        slog.Logger
	log           slog.Logger
	iapiLog       slog.Logger
	pubsubLog     slog.Logger
	xcBotLog      slog.Logger
	agendasLog    slog.Logger
	proposalsLog  slog.Logger

	// filled after init so setLogLevels works
	subsystemLoggers map[string]slog.Logger
)

// -------------------- Split backend --------------------

// splitBackend implements slog.Backend and routes Debug/Trace to debugWriter,
// and Info/Warn/Error/Critical to infoWriter.
type splitBackend struct {
	infoWriter  io.Writer
	debugWriter io.Writer
	mu          sync.Mutex
}

// splitLogger implements slog.Logger
type splitLogger struct {
	subsystem string
	backend   *splitBackend
	level     slog.Level
}

// NewSplitBackend creates a backend that writes to separate writers by level.
// You can pass io.MultiWriter(os.Stdout, rotator) if you also want stdout.
func NewSplitBackend(infoWriter, debugWriter io.Writer) *splitBackend {
	return &splitBackend{
		infoWriter:  infoWriter,
		debugWriter: debugWriter,
	}
}

// Logger implements slog.Backend. Default level = Info.
func (b *splitBackend) Logger(subsystem string) slog.Logger {
	return &splitLogger{
		subsystem: subsystem,
		backend:   b,
		level:     slog.LevelInfo,
	}
}

func (l *splitLogger) SetLevel(level slog.Level) { l.level = level }
func (l *splitLogger) Level() slog.Level         { return l.level }

func (l *splitLogger) write(w io.Writer, level string, format string, args ...interface{}) {
	if w == nil {
		// fail-safe to stderr if writer is nil
		fmt.Fprintf(os.Stderr, "logger writer is nil for level %s [%s]\n", level, l.subsystem)
		return
	}
	l.backend.mu.Lock()
	defer l.backend.mu.Unlock()

	ts := time.Now().Format("2006-01-02 15:04:05.000")
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(w, "%s [%s] %s: %s\n", ts, level, l.subsystem, msg)
}

// ---- implement full slog.Logger interface ----

// Trace
func (l *splitLogger) Trace(args ...interface{}) {
	if l.level <= slog.LevelTrace {
		l.write(l.backend.debugWriter, "TRC", "%s", fmt.Sprint(args...))
	}
}
func (l *splitLogger) Tracef(format string, args ...interface{}) {
	if l.level <= slog.LevelTrace {
		l.write(l.backend.debugWriter, "TRC", format, args...)
	}
}

// Debug
func (l *splitLogger) Debug(args ...interface{}) {
	if l.level <= slog.LevelDebug {
		l.write(l.backend.debugWriter, "DBG", "%s", fmt.Sprint(args...))
	}
}
func (l *splitLogger) Debugf(format string, args ...interface{}) {
	if l.level <= slog.LevelDebug {
		l.write(l.backend.debugWriter, "DBG", format, args...)
	}
}

// Info
func (l *splitLogger) Info(args ...interface{}) {
	if l.level <= slog.LevelInfo {
		l.write(l.backend.infoWriter, "INF", "%s", fmt.Sprint(args...))
	}
}
func (l *splitLogger) Infof(format string, args ...interface{}) {
	if l.level <= slog.LevelInfo {
		l.write(l.backend.infoWriter, "INF", format, args...)
	}
}

// Warn
func (l *splitLogger) Warn(args ...interface{}) {
	if l.level <= slog.LevelWarn {
		l.write(l.backend.infoWriter, "WRN", "%s", fmt.Sprint(args...))
	}
}
func (l *splitLogger) Warnf(format string, args ...interface{}) {
	if l.level <= slog.LevelWarn {
		l.write(l.backend.infoWriter, "WRN", format, args...)
	}
}

// Error
func (l *splitLogger) Error(args ...interface{}) {
	if l.level <= slog.LevelError {
		l.write(l.backend.infoWriter, "ERR", "%s", fmt.Sprint(args...))
	}
}
func (l *splitLogger) Errorf(format string, args ...interface{}) {
	if l.level <= slog.LevelError {
		l.write(l.backend.infoWriter, "ERR", format, args...)
	}
}

// Critical
func (l *splitLogger) Critical(args ...interface{}) {
	if l.level <= slog.LevelCritical {
		l.write(l.backend.infoWriter, "CRT", "%s", fmt.Sprint(args...))
	}
}
func (l *splitLogger) Criticalf(format string, args ...interface{}) {
	if l.level <= slog.LevelCritical {
		l.write(l.backend.infoWriter, "CRT", format, args...)
	}
}

// -------------------- Init / helpers --------------------

// initLogRotators creates two rotating files and wires the backend & loggers.
// logPath: path to info/error file; debugLogPath: path to debug file.
func initLogRotators(logPath, debugLogPath string, maxRolls int) {
	// Ensure dirs
	if err := os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create log directory: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(debugLogPath), 0700); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create debug log directory: %v\n", err)
		os.Exit(1)
	}

	// Rotators
	var err error
	infoRotator, err = rotator.New(logPath, 32*1024, false, maxRolls)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create info log rotator: %v\n", err)
		os.Exit(1)
	}
	debugRotator, err = rotator.New(debugLogPath, 32*1024, false, maxRolls)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create debug log rotator: %v\n", err)
		os.Exit(1)
	}

	// Writers
	infoWriter := io.MultiWriter(os.Stdout, infoRotator)
	debugWriter := io.MultiWriter(os.Stdout, debugRotator)

	// Backend
	backendLog = NewSplitBackend(infoWriter, debugWriter)

	// Subsystem loggers (create instance)
	notifyLog = backendLog.Logger("NTFN")
	postgresqlLog = backendLog.Logger("PSQL")
	stakedbLog = backendLog.Logger("SKDB")
	BlockdataLog = backendLog.Logger("BLKD")
	clientLog = backendLog.Logger("RPCC")
	mempoolLog = backendLog.Logger("MEMP")
	expLog = backendLog.Logger("EXPR")
	apiLog = backendLog.Logger("JAPI")
	log = backendLog.Logger("DATD")
	iapiLog = backendLog.Logger("IAPI")
	pubsubLog = backendLog.Logger("PUBS")
	xcBotLog = backendLog.Logger("XBOT")
	agendasLog = backendLog.Logger("AGDB")
	proposalsLog = backendLog.Logger("PRDB")

	all := []slog.Logger{
		notifyLog, postgresqlLog, stakedbLog, BlockdataLog, clientLog,
		mempoolLog, expLog, apiLog, log, iapiLog, pubsubLog,
		xcBotLog, agendasLog, proposalsLog,
	}
	for _, lg := range all {
		lg.SetLevel(slog.LevelDebug)
	}

	// Wire external packages After turn on debug
	dcrpg.UseLogger(postgresqlLog)
	stakedb.UseLogger(stakedbLog)
	blockdata.UseLogger(BlockdataLog)
	rpcclient.UseLogger(clientLog)
	rpcutils.UseLogger(clientLog)
	mempool.UseLogger(mempoolLog)
	explorer.UseLogger(expLog)
	api.UseLogger(apiLog)
	insight.UseLogger(iapiLog)
	middleware.UseLogger(apiLog)
	notify.UseLogger(notifyLog)
	pubsub.UseLogger(pubsubLog)
	exchanges.UseLogger(xcBotLog)
	agendas.UseLogger(agendasLog)
	politeia.UseLogger(proposalsLog)

	// Save map to use setLogLevels laters
	subsystemLoggers = map[string]slog.Logger{
		"NTFN": notifyLog,
		"PSQL": postgresqlLog,
		"SKDB": stakedbLog,
		"BLKD": BlockdataLog,
		"RPCC": clientLog,
		"MEMP": mempoolLog,
		"EXPR": expLog,
		"JAPI": apiLog,
		"IAPI": iapiLog,
		"DATD": log,
		"PUBS": pubsubLog,
		"XBOT": xcBotLog,
		"AGDB": agendasLog,
		"PRDB": proposalsLog,
	}
}

// Call this on shutdown.
func closeLogRotators() {
	if infoRotator != nil {
		_ = infoRotator.Close()
	}
	if debugRotator != nil {
		_ = debugRotator.Close()
	}
}

// setLogLevel sets the logging level for a subsystem.
func setLogLevel(subsystemID string, logLevel string) {
	logger, ok := subsystemLoggers[subsystemID]
	if !ok {
		return
	}
	level, _ := slog.LevelFromString(logLevel) // defaults to info on invalid
	logger.SetLevel(level)
}

// setLogLevels sets the log level for all subsystems.
func setLogLevels(logLevel string) {
	for subsystemID := range subsystemLoggers {
		setLogLevel(subsystemID, logLevel)
	}
}
