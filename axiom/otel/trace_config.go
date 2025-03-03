package otel

import (
	"time"

	"github.com/axiomhq/axiom-go/internal/config"
)

const defaultTraceAPIEndpoint = "/api/v1/traces"

type traceConfig struct {
	config.Config

	// APIEndpoint is the endpoint to use for the trace exporter.
	APIEndpoint string
	// Timeout is the timeout for the trace exporters underlying http client.
	Timeout time.Duration
	// NoEnv disables the use of AXIOM_* environment variables.
	NoEnv bool
}

func defaultTraceConfig() traceConfig {
	return traceConfig{
		Config:      config.Default(),
		APIEndpoint: defaultTraceAPIEndpoint,
	}
}

// A TraceOption modifies the behaviour of OpenTelemetry traces. Nonetheless,
// the official OTEL_* environment variables are preferred over the options or
// AXIOM_* environment variables.
type TraceOption func(c *traceConfig) error

// SetURL sets the base URL used by the client.
//
// Can also be specified using the `AXIOM_URL` environment variable.
func SetURL(baseURL string) TraceOption {
	return func(c *traceConfig) error { return c.Options(config.SetURL(baseURL)) }
}

// SetAccessToken specifies the access token to use.
//
// Can also be specified using the `AXIOM_TOKEN` environment variable.
func SetAccessToken(accessToken string) TraceOption {
	return func(c *traceConfig) error { return c.Options(config.SetAccessToken(accessToken)) }
}

// SetOrganizationID specifies the organization ID to use when connecting to
// Axiom. When a personal access token is used, this method can be used to
// switch between organizations without creating a new client instance.
//
// Can also be specified using the `AXIOM_ORG_ID` environment variable.
func SetOrganizationID(organizationID string) TraceOption {
	return func(c *traceConfig) error { return c.Options(config.SetOrganizationID(organizationID)) }
}

// SetAPIEndpoint sets the api endpoint used by the client.
func SetAPIEndpoint(path string) TraceOption {
	return func(c *traceConfig) error {
		c.APIEndpoint = path
		return nil
	}
}

// SetTimeout sets the http timeout used by the client.
func SetTimeout(timeout time.Duration) TraceOption {
	return func(c *traceConfig) error {
		c.Timeout = timeout
		return nil
	}
}

// SetNoEnv prevents the client from deriving its configuration from the
// environment (AXIOM_* environment variables).
func SetNoEnv() TraceOption {
	return func(c *traceConfig) error {
		c.NoEnv = true
		return nil
	}
}
