package daemon

import (
	"log/slog"

	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/storage"
	"gorm.io/gorm"
)

// Shared holds shared resources for the daemon process.
type Shared struct {
	DB              *gorm.DB
	Storage         *storage.DB
	Config          *config.Config
	Logger          *slog.Logger
	NewSessionStore SessionStoreFactory

	// InstanceID identifies this daemon process (a boot nonce). It is
	// reported to clients so they can tell a transient socket drop (same
	// process, in-flight state intact) from a restart (new process, all
	// in-flight state lost) — see internal/rpc.PingResult.
	InstanceID string
}
