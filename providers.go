package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/afittestide/asimi/court"
	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/storage"
	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/fx"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// slogGormLogger wraps slog.Logger to implement gormlogger.Interface
type slogGormLogger struct {
	logger   *slog.Logger
	logLevel gormlogger.LogLevel
}

func newSlogGormLogger(logger *slog.Logger, level gormlogger.LogLevel) *slogGormLogger {
	return &slogGormLogger{logger: logger, logLevel: level}
}

func (l *slogGormLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	return &slogGormLogger{logger: l.logger, logLevel: level}
}

func (l *slogGormLogger) Info(ctx context.Context, msg string, args ...interface{}) {
	if l.logLevel >= gormlogger.Info {
		l.logger.Info(fmt.Sprintf(msg, args...), "source", "gorm")
	}
}

func (l *slogGormLogger) Warn(ctx context.Context, msg string, args ...interface{}) {
	if l.logLevel >= gormlogger.Warn {
		l.logger.Warn(fmt.Sprintf(msg, args...), "source", "gorm")
	}
}

func (l *slogGormLogger) Error(ctx context.Context, msg string, args ...interface{}) {
	if l.logLevel >= gormlogger.Error {
		l.logger.Error(fmt.Sprintf(msg, args...), "source", "gorm")
	}
}

func (l *slogGormLogger) Trace(ctx context.Context, begin time.Time, fc func() (sql string, rowsAffected int64), err error) {
	if l.logLevel <= gormlogger.Silent {
		return
	}
	/*
		elapsed := time.Since(begin)
		sql, rows := fc()
		if err != nil {
			l.logger.Debug("gorm trace", "source", "gorm", "sql", sql, "rows", rows, "elapsed", elapsed, "error", err)
		} else {
			l.logger.Debug("gorm trace", "source", "gorm", "sql", sql, "rows", rows, "elapsed", elapsed)
		}
	*/
}

// LoggerResult holds the configured logger
type LoggerResult struct {
	fx.Out
	Logger *slog.Logger
}

// ProvideLogger creates and returns a logger instance
func ProvideLogger() (LoggerResult, error) {
	initLogger()
	return LoggerResult{
		Logger: slog.Default(),
	}, nil
}

// ProvideConfig loads user-level and project-level configuration for the daemon.
// It reads built-in defaults + ~/.config/asimi/asimi.conf + project-level
// .agents/asimi.conf, and resolves API keys from environment variables.
func ProvideConfig(logger *slog.Logger, repoInfo repo.RepoInfo) (*Config, error) {
	logger.Info("loading configuration")

	// Ensure user config file exists (creates it on first run)
	created, err := config.EnsureUserConfigExists()
	if err != nil {
		logger.Warn("failed to ensure user config exists", "error", err)
	} else if created {
		logger.Info("created user config file on first run")
		config.ConfigCreated = true
	}

	cfg, err := config.LoadProjectConfig(repoInfo.ProjectRoot, true)
	if err != nil {
		return nil, fmt.Errorf("failed to load configuration: %w", err)
	}
	// Override from CLI flag
	if cli.NoCleanup {
		cfg.Sandbox.NoCleanup = true
	}
	if cli.MaxTurns > 0 {
		cfg.LLM.MaxTurns = cli.MaxTurns
	}
	if cli.IsolatedHost {
		cfg.Court.IsolatedHost = true
	}
	if cli.Model != "" {
		cfg.LLM.Model = cli.Model
	}
	if cli.Provider != "" {
		cfg.LLM.Provider = cli.Provider
	}
	if cli.ReasoningEffort != "" {
		if err := config.ValidateReasoningEffort(cli.ReasoningEffort); err != nil {
			return nil, err
		}
		cfg.LLM.ReasoningEffort = cli.ReasoningEffort
	}
	logger.Info("configuration loaded")
	return cfg, nil
}

// StorageParams holds parameters for storage initialization
type StorageParams struct {
	fx.In
	Lifecycle fx.Lifecycle
	Config    *Config
	Logger    *slog.Logger
}

// StorageResult holds the storage initialization result
type StorageResult struct {
	fx.Out
	DB *storage.DB
}

// ProvideStorage initializes the SQLite storage database
func ProvideStorage(params StorageParams) (StorageResult, error) {
	params.Logger.Info("initializing storage", "database_path", params.Config.Storage.DatabasePath)
	db, err := storage.InitDB(params.Config.Storage.DatabasePath)
	if err != nil {
		params.Logger.Error("failed to initialize storage", "error", err)
		return StorageResult{}, fmt.Errorf("failed to initialize storage: %w", err)
	}
	params.Logger.Info("storage initialized successfully")

	// Register cleanup on shutdown
	params.Lifecycle.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			params.Logger.Info("closing storage")
			if err := db.Close(); err != nil {
				params.Logger.Error("failed to close storage", "error", err)
				return err
			}
			params.Logger.Info("storage closed successfully")
			return nil
		},
	})

	return StorageResult{DB: db}, nil
}

// ProvideRepoInfo returns information about the git repository
func ProvideRepoInfo(logger *slog.Logger) repo.RepoInfo {
	logger.Info("detecting git repository")
	repoInfo := repo.GetRepoInfo()
	if repoInfo.ProjectRoot != "" {
		logger.Info("git repository detected", "root", repoInfo.ProjectRoot, "branch", repoInfo.Branch)
	} else {
		logger.Info("no git repository found")
	}
	return repoInfo
}

// ProvideScheduler creates the tool scheduler for dependency injection
func ProvideScheduler() *runners.CoreToolScheduler {
	return runners.NewCoreToolScheduler(nil)
}

// ShellRunnerParams holds parameters for shell runner initialization
type ShellRunnerParams struct {
	fx.In
	Lifecycle fx.Lifecycle
	Config    *Config
	RepoInfo  repo.RepoInfo
	Scheduler *runners.CoreToolScheduler
	Logger    *slog.Logger
}

// ProvideShellRunner creates and returns a shell runner with proper lifecycle management
func ProvideShellRunner(params ShellRunnerParams) runners.Runner {
	params.Logger.Info("initializing shell runner")

	var runner runners.Runner
	if params.Config.Court.IsolatedHost {
		runner = runners.NewHostRunner(0, params.RepoInfo.ProjectRoot)
		params.Logger.Info("using host runner (isolated-host mode)")
	} else {
		runner = runners.InitShellRunner(&params.Config.Sandbox, params.RepoInfo)
	}

	params.Logger.Info("shell runner initialized", "type", runner.RunnerType())

	// Register cleanup hook to close the shell runner when app stops
	params.Lifecycle.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			params.Logger.Info("shutting down shell runner")
			err := runner.Close(ctx)
			r := runners.GetRunner()
			if r != nil && runner != r {
				err = errors.Join(err, r.Close(ctx))
			}
			return err
		},
	})

	return runner
}

// PromptHistoryResult holds the prompt history store
type PromptHistoryResult struct {
	fx.Out
	History *PromptHistory `name:"prompt"`
}

// CommandHistoryResult holds the command history store
type CommandHistoryResult struct {
	fx.Out
	History *CommandHistory `name:"command"`
}

// ProvidePromptHistory creates and returns the prompt history store
func ProvidePromptHistory(db *storage.DB, repoInfo repo.RepoInfo, logger *slog.Logger) (PromptHistoryResult, error) {
	logger.Info("loading prompt history")
	historyStore, err := NewPromptHistoryStore(db, repoInfo)
	if err != nil {
		logger.Warn("failed to initialize prompt history store", "error", err)
		return PromptHistoryResult{History: nil}, nil // Don't fail, just return nil
	}
	return PromptHistoryResult{History: historyStore}, nil
}

// ProvideCommandHistory creates and returns the command history store
func ProvideCommandHistory(db *storage.DB, repoInfo repo.RepoInfo, logger *slog.Logger) (CommandHistoryResult, error) {
	logger.Info("loading command history")
	historyStore, err := NewCommandHistoryStore(db, repoInfo)
	if err != nil {
		logger.Warn("failed to initialize command history store", "error", err)
		return CommandHistoryResult{History: nil}, nil // Don't fail, just return nil
	}
	return CommandHistoryResult{History: historyStore}, nil
}

// ProvideSessionHistory creates and returns the session history store
func ProvideSessionHistory(db *storage.DB, cfg *Config, repoInfo repo.RepoInfo, logger *slog.Logger) (*SessionStore, error) {
	if !cfg.Session.Enabled {
		return nil, nil // Session storage is disabled
	}

	logger.Info("loading session history")
	defaults := config.DefaultSessionConfig()
	maxSessions := defaults.MaxSessions
	maxAgeDays := defaults.MaxAgeDays
	if cfg.Session.MaxSessions > 0 {
		maxSessions = cfg.Session.MaxSessions
	}
	if cfg.Session.MaxAgeDays > 0 {
		maxAgeDays = cfg.Session.MaxAgeDays
	}

	store, err := NewSessionStore(db, repoInfo, maxSessions, maxAgeDays)
	if err != nil {
		logger.Error("failed to create session store", "error", err)
		return nil, nil // Don't fail startup
	}
	return store, nil
}

// TUIModelParams holds parameters for TUI model creation
type TUIModelParams struct {
	fx.In
	Config         *Config
	RepoInfo       repo.RepoInfo
	PromptHistory  *PromptHistory  `name:"prompt"`
	CommandHistory *CommandHistory `name:"command"`
	SessionStore   *SessionStore
	DB             *storage.DB
	Scheduler      *runners.CoreToolScheduler
	Court          *court.Court
	Logger         *slog.Logger
}

// ProvideTUIModel creates and returns the TUI model
func ProvideTUIModel(params TUIModelParams) *TUIModel {
	return NewTUIModel(params.Config, &params.RepoInfo, params.PromptHistory, params.CommandHistory, params.SessionStore, params.DB, params.Scheduler, params.Court)
}

// TUIProgramParams holds parameters for TUI program initialization
type TUIProgramParams struct {
	fx.In
	Model     *TUIModel
	Lifecycle fx.Lifecycle
	Logger    *slog.Logger
}

// StartTUI creates the TUI program
func StartTUI(params TUIProgramParams) *tea.Program {
	params.Logger.Info("creating TUI program")

	// Create the bubbletea program with alt screen and mouse support
	prog := tea.NewProgram(params.Model, tea.WithAltScreen(), tea.WithMouseCellMotion())

	// Set global program reference so async operations can send messages
	program = prog

	return prog
}

// GormDBParams holds parameters for GORM database initialization
type GormDBParams struct {
	fx.In
	DB     *storage.DB
	Logger *slog.Logger
}

// ProvideGormDB wraps the existing SQL connection in GORM
func ProvideGormDB(params GormDBParams) (*gorm.DB, error) {
	params.Logger.Info("initializing GORM database (reusing existing connection)")

	// Configure GORM logger to use slog (writes to log file, not stdout)
	var gormLog gormlogger.Interface
	if cli.Debug {
		gormLog = newSlogGormLogger(params.Logger, gormlogger.Info)
	} else {
		gormLog = newSlogGormLogger(params.Logger, gormlogger.Silent)
	}

	db, err := gorm.Open(sqlite.New(sqlite.Config{
		Conn: params.DB.Conn(),
	}), &gorm.Config{
		Logger: gormLog,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open GORM database: %w", err)
	}

	// Rename edict_id → id in edicts table (if the old column exists)
	if db.Migrator().HasColumn(&storage.Edict{}, "edict_id") {
		if err := db.Migrator().RenameColumn(&storage.Edict{}, "edict_id", "id"); err != nil {
			return nil, fmt.Errorf("failed to rename edict_id column: %w", err)
		}
	}

	// Auto-migrate Court tables
	if err := db.AutoMigrate(
		&storage.Edict{},
		&storage.Seal{},
		&storage.Zhengming{},
		&storage.TianEvent{},
		&storage.TianEventDLQ{},
		&storage.Ling{},
		&storage.ForgeManifest{},
		&storage.JudgeVerdict{},
		&storage.CensorPrecedent{},
		&storage.Incident{},
		&storage.RulerCouncil{},
		&storage.RitualGuardCheckpoint{},
		&court.RitualExecution{},
		&court.RitualStepState{},
	); err != nil {
		return nil, fmt.Errorf("failed to migrate Court schema: %w", err)
	}

	if err := storage.PostGormMigrate(db); err != nil {
		return nil, fmt.Errorf("failed post-GORM migration: %w", err)
	}

	params.Logger.Info("GORM database initialized")
	return db, nil
}

// CourtParams holds parameters for Court initialization
type CourtParams struct {
	fx.In
	Lifecycle    fx.Lifecycle
	GormDB       *gorm.DB
	Config       *Config
	RepoInfo     repo.RepoInfo
	Runner       runners.Runner
	Logger       *slog.Logger
	SessionStore *SessionStore `optional:"true"`
}

// ProvideCourt creates the Court coordinator with lifecycle management
func ProvideCourt(params CourtParams) *court.Court {

	// Start with defaults, then overlay config file values
	cfg := config.DefaultCourtConfig()
	if params.Config.Court.Username != "" {
		cfg.Username = params.Config.Court.Username
	}
	if params.Config.Court.Project != "" {
		cfg.Project = params.Config.Court.Project
	} else if params.RepoInfo.Slug != "" {
		cfg.Project = params.RepoInfo.Slug
	}
	cfg.IsolatedHost = params.Config.Court.IsolatedHost
	if params.Config.Court.OutputLimit > 0 {
		cfg.OutputLimit = params.Config.Court.OutputLimit
	}
	params.Logger.Info("initializing Court", "user", cfg.Username, "project", cfg.Project)

	s := court.NewCourt(params.GormDB, cfg, params.Runner, params.Logger)
	// notify is set later via s.SetNotify(program.Send) once the TUI program is created

	// Persist sessions to the DB as messages are added. Every minister
	// that owns a UI tab (forge, sage, judge, chancellor) gets the
	// persister attached when it creates an interactive session; ephemeral
	// ritual-task sessions don't receive one and skip storage. Wiring here
	// covers both the daemon binary and the in-process TUI — same provider.
	if params.SessionStore != nil {
		s.SetSessionPersister(params.SessionStore)
	}

	// Register lifecycle hooks
	params.Lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			params.Logger.Info("starting Court")
			s.SetRepoInfo(params.RepoInfo)
			return s.Start(ctx)
		},
		OnStop: func(ctx context.Context) error {
			params.Logger.Info("stopping Court")
			return s.Stop()
		},
	})

	return s
}
