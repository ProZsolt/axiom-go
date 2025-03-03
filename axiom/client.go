package axiom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/klauspost/compress/gzhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/httptrace/otelhttptrace"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/axiomhq/axiom-go/internal/config"
	"github.com/axiomhq/axiom-go/internal/version"
)

const (
	headerAuthorization  = "Authorization"
	headerOrganizationID = "X-Axiom-Org-Id"

	headerAccept      = "Accept"
	headerContentType = "Content-Type"
	headerUserAgent   = "User-Agent"

	defaultMediaType = "application/octet-stream"
	mediaTypeJSON    = "application/json"
	mediaTypeNDJSON  = "application/x-ndjson"

	otelTracerName = "github.com/axiomhq/axiom-go/axiom"
)

var validOnlyAPITokenPaths = regexp.MustCompile(`^/api/v1/datasets/([^/]+/(ingest|query)|_apl)(\?.+)?$`)

// service is the base service used by all Axiom API services.
type service struct {
	client   *Client
	basePath string
}

// DefaultHTTPClient returns the default HTTP client used for making requests.
func DefaultHTTPClient() *http.Client {
	return &http.Client{
		Transport: DefaultHTTPTransport(),
	}
}

// DefaultHTTPTransport returns the default HTTP transport used for the default
// HTTP client.
func DefaultHTTPTransport() http.RoundTripper {
	return otelhttp.NewTransport(gzhttp.Transport(&http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 5 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second,
		ForceAttemptHTTP2:   true,
	}))
}

// Client provides the Axiom HTTP API operations.
type Client struct {
	config config.Config

	httpClient     *http.Client
	userAgent      string
	strictDecoding bool
	noEnv          bool
	noLimiting     bool

	tracer trace.Tracer

	// Services for communicating with different parts of the GitHub API.
	Datasets      *DatasetsService
	Organizations *OrganizationsService
	Users         *UsersService
}

// NewClient returns a new Axiom API client. It automatically takes its
// configuration from the environment. To connect, export the following
// environment variables:
//
//   - AXIOM_TOKEN
//   - AXIOM_ORG_ID (only when using a personal token)

// The configuration can be set manually using `Option` functions prefixed with
// `Set`.
//
// The access token must be an api or personal token which can be created on
// the settings or user profile page on Axiom.
func NewClient(options ...Option) (*Client, error) {
	client := &Client{
		config: config.Default(),

		userAgent: "axiom-go",

		httpClient: DefaultHTTPClient(),

		tracer: otel.Tracer(otelTracerName),
	}

	// Include module version in the user agent.
	if v := version.Get(); v != "" {
		client.userAgent += fmt.Sprintf("/%s", v)
	}

	client.Datasets = &DatasetsService{client, "/api/v1/datasets"}
	client.Organizations = &OrganizationsService{client, "/api/v1/orgs"}
	client.Users = &UsersService{client, "/api/v1/users"}

	// Apply supplied options.
	if err := client.Options(options...); err != nil {
		return nil, err
	}

	// Make sure to populate remaining fields from the environment, if not
	// explicitly disabled.
	if !client.noEnv {
		if err := client.config.IncorporateEnvironment(); err != nil {
			return nil, err
		}
	}

	return client, client.config.Validate()
}

// Options applies Options to the Client.
func (c *Client) Options(options ...Option) error {
	for _, option := range options {
		if err := option(c); err != nil {
			return err
		}
	}
	return nil
}

// ValidateCredentials makes sure the client can properly authenticate against
// the configured Axiom deployment.
func (c *Client) ValidateCredentials(ctx context.Context) error {
	if config.IsPersonalToken(c.config.AccessToken()) {
		_, err := c.Users.Current(ctx)
		return err
	}

	// FIXME(lukasmalkmus): Well, with the current API, we need to assume the
	// token is valid.
	// return ErrInvalidToken
	return nil
}

// Call creates a new API request and executes it. The response body is JSON
// decoded or directly written to v, depending on v being an io.Writer or not.
func (c *Client) Call(ctx context.Context, method, path string, body, v any) error {
	req, err := c.NewRequest(ctx, method, path, body)
	if err != nil {
		return err
	} else if _, err = c.Do(req, v); err != nil {
		return err
	}
	return nil
}

// NewRequest creates an API request. If specified, the value pointed to by body
// will be included as the request body. If it is not an io.Reader, it will be
// included as a JSON encoded request body.
func (c *Client) NewRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	rel, err := url.ParseRequestURI(path)
	if err != nil {
		return nil, err
	}
	endpoint := c.config.BaseURL().ResolveReference(rel)

	if config.IsAPIToken(c.config.AccessToken()) && !validOnlyAPITokenPaths.MatchString(endpoint.Path) {
		return nil, ErrUnprivilegedToken
	}

	var (
		r        io.Reader
		isReader bool
	)
	if body != nil {
		if r, isReader = body.(io.Reader); !isReader {
			buf := new(bytes.Buffer)
			if err = json.NewEncoder(buf).Encode(body); err != nil {
				return nil, err
			}
			r = buf
		}
	}

	ctx = httptrace.WithClientTrace(ctx, otelhttptrace.NewClientTrace(ctx))
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), r)
	if err != nil {
		return nil, err
	}

	// Set Content-Type.
	if body != nil && !isReader {
		req.Header.Set(headerContentType, mediaTypeJSON)
	} else if body != nil {
		req.Header.Set(headerContentType, defaultMediaType)
	}

	// Set authorization header, if present.
	if c.config.AccessToken() != "" {
		req.Header.Set(headerAuthorization, "Bearer "+c.config.AccessToken())
	}

	// Set organization ID header when using a personal token.
	if config.IsPersonalToken(c.config.AccessToken()) && c.config.OrganizationID() != "" {
		req.Header.Set(headerOrganizationID, c.config.OrganizationID())
	}

	// Set other headers.
	req.Header.Set(headerAccept, mediaTypeJSON)
	req.Header.Set(headerUserAgent, c.userAgent)

	return req, nil
}

// Do sends an API request and returns the API response. The response body is
// JSON decoded or directly written to v, depending on v being an io.Writer or
// not.
func (c *Client) Do(req *http.Request, v any) (*Response, error) {
	bck := backoff.NewExponentialBackOff()
	bck.InitialInterval = 200 * time.Millisecond
	bck.Multiplier = 2.0
	bck.MaxElapsedTime = 10 * time.Second

	var resp *Response
	err := backoff.Retry(func() error {
		httpResp, err := c.httpClient.Do(req)
		if err != nil {
			return err
		}

		resp = newResponse(httpResp)

		// We should only retry in the case the status code is >= 500, anything below isn't worth retrying.
		if code := resp.StatusCode; code >= 500 {
			return fmt.Errorf("got status code %d", code)
		}

		return nil
	}, bck)

	defer func() {
		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()

	if err != nil {
		return resp, err
	}

	if statusCode := resp.StatusCode; statusCode >= 400 {
		// Handle common http status codes by returning proper errors so it is
		// possible to check for them using `errors.Is()`.
		switch statusCode {
		case http.StatusUnauthorized:
			return resp, ErrUnauthenticated
		case http.StatusForbidden:
			return resp, ErrUnauthorized
		case http.StatusNotFound:
			return resp, ErrNotFound
		case http.StatusConflict:
			return resp, ErrExists
		case http.StatusTooManyRequests, httpStatusLimitExceeded:
			return resp, &LimitError{
				Limit: resp.Limit,

				response: resp.Response,
			}
		}

		// Handle a generic HTTP error if the response is not JSON formatted.
		if val := resp.Header.Get(headerContentType); !strings.HasPrefix(val, mediaTypeJSON) {
			return resp, &Error{
				Status:  statusCode,
				Message: http.StatusText(statusCode),
			}
		}

		// For error handling, we want to have access to the raw request body
		// to inspect it further
		var (
			buf bytes.Buffer
			dec = json.NewDecoder(io.TeeReader(resp.Body, &buf))
		)

		// Handle a properly JSON formatted Axiom API error response.
		errResp := &Error{Status: statusCode}
		if err = dec.Decode(&errResp); err != nil {
			return resp, fmt.Errorf("error decoding %d error response: %w", statusCode, err)
		}

		// In case something went wrong, include the raw response and hope for
		// the best.
		if errResp.Message == "" {
			s := strings.ReplaceAll(buf.String(), "\n", " ")
			errResp.Message = s
		}

		return resp, errResp
	}

	if v != nil {
		if w, ok := v.(io.Writer); ok {
			_, err = io.Copy(w, resp.Body)
			return resp, err
		}

		if val := resp.Header.Get(headerContentType); strings.HasPrefix(val, mediaTypeJSON) {
			dec := json.NewDecoder(resp.Body)
			if c.strictDecoding {
				dec.DisallowUnknownFields()
			}
			return resp, dec.Decode(v)
		}

		return resp, errors.New("cannot decode response with unknown content type")
	}

	return resp, nil
}

func (c *Client) trace(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return c.tracer.Start(ctx, name, opts...)
}

func spanError(span trace.Span, err error) error {
	if err == nil {
		return nil
	}

	if span.IsRecording() {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
	}

	return err
}
