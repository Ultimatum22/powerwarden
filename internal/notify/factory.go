package notify

import (
	"fmt"
	"net/http"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/config"
)

// New builds the Notifier selected by cfg.Provider. cfg is assumed to have
// already passed config.Validate.
func New(cfg config.Notify, token string) (Notifier, error) {
	switch cfg.Provider {
	case "ntfy":
		return Ntfy{
			URL:        cfg.URL,
			Token:      token,
			HTTPClient: &http.Client{Timeout: 10 * time.Second},
		}, nil
	default:
		return nil, fmt.Errorf("notify: unknown provider %q", cfg.Provider)
	}
}
