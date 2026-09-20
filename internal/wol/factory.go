package wol

import (
	"fmt"
	"net/http"
	"time"

	"github.com/Ultimatum22/powerwarden/internal/config"
)

// New builds the Sender selected by cfg.Method. cfg is assumed to have
// already passed config.Validate.
func New(cfg config.WoL) (Sender, error) {
	switch cfg.Method {
	case "unicast":
		return UnicastSender{Target: cfg.Target}, nil
	case "broadcast":
		return BroadcastSender{Target: cfg.Target}, nil
	case "router_api":
		return RouterAPISender{
			URL:     cfg.RouterAPI.URL,
			Method:  cfg.RouterAPI.Method,
			Headers: cfg.RouterAPI.Headers,
			Body:    cfg.RouterAPI.Body,
			HTTPClient: &http.Client{
				Timeout: 10 * time.Second,
			},
		}, nil
	default:
		return nil, fmt.Errorf("wol: unknown method %q", cfg.Method)
	}
}
