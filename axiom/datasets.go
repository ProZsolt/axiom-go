package axiom

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
	"unicode"

	"github.com/klauspost/compress/zstd"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/axiomhq/axiom-go/axiom/ingest"
	"github.com/axiomhq/axiom-go/axiom/query"
	"github.com/axiomhq/axiom-go/axiom/querylegacy"
)

//go:generate go run golang.org/x/tools/cmd/stringer -type=ContentType,ContentEncoding -linecomment -output=datasets_string.go

var (
	// ErrUnknownContentType is raised when the given content type is not valid.
	ErrUnknownContentType = errors.New("unknown content type")
	// ErrUnknownContentEncoding is raised when the given content encoding is
	// not valid.
	ErrUnknownContentEncoding = errors.New("unknown content encoding")
)

// ContentType describes the content type of the data to ingest.
type ContentType uint8

const (
	// JSON treats the data as JSON array.
	JSON ContentType = iota + 1 // application/json
	// NDJSON treats the data as newline delimited JSON objects. Preferred
	// data format.
	NDJSON // application/x-ndjson
	// CSV treats the data as CSV content.
	CSV // text/csv
)

// ContentEncoding describes the content encoding of the data to ingest.
type ContentEncoding uint8

const (
	// Identity marks the data as not being encoded.
	Identity ContentEncoding = iota + 1 //
	// Gzip marks the data as being gzip encoded. Preferred compression format.
	Gzip // gzip
	// Zstd marks the data as being zstd encoded.
	Zstd // zstd
)

// An Event is a map of key-value pairs.
type Event map[string]any

// Dataset represents an Axiom dataset.
type Dataset struct {
	// ID of the dataset.
	ID string `json:"id"`
	// Name is the unique name of the dataset.
	Name string `json:"name"`
	// Description of the dataset.
	Description string `json:"description"`
	// CreatedBy is the ID of the user who created the dataset.
	CreatedBy string `json:"who"`
	// CreatedAt is the time the dataset was created at.
	CreatedAt time.Time `json:"created"`
}

// TrimResult is the result of a trim operation.
type TrimResult struct {
	// BlocksDeleted is the amount of blocks deleted by the trim operation.
	//
	// Deprecated: BlocksDeleted is deprecated and will be removed in the
	// future.
	BlocksDeleted int `json:"numDeleted"`
}

// DatasetCreateRequest is a request used to create a dataset.
type DatasetCreateRequest struct {
	// Name of the dataset to create. Restricted to 80 characters of [a-zA-Z0-9]
	// and special characters "-", "_" and ".". Special characters cannot be a
	// prefix or suffix. The prefix cannot be "axiom-".
	Name string `json:"name"`
	// Description of the dataset to create.
	Description string `json:"description"`
}

// DatasetUpdateRequest is a request used to update a dataset.
type DatasetUpdateRequest struct {
	// Description of the dataset to update.
	Description string `json:"description"`
}

type wrappedDataset struct {
	Dataset

	// HINT(lukasmalkmus) This is some future stuff we don't yet support in this
	// package so we just ignore it for now.
	IntegrationConfigs any `json:"integrationConfigs,omitempty"`
	IntegrationFilters any `json:"integrationFilters,omitempty"`
	QuickQueries       any `json:"quickQueries,omitempty"`
}

type datasetTrimRequest struct {
	// MaxDuration marks the oldest timestamp an event can have before getting
	// deleted.
	MaxDuration string `json:"maxDuration"`
}

type aplQueryRequest struct {
	// Query is the APL query string.
	Query string `json:"apl"`
	// StartTime of the query. Optional.
	StartTime time.Time `json:"startTime"`
	// EndTime of the query. Optional.
	EndTime time.Time `json:"endTime"`
}

// DatasetsService handles communication with the dataset related operations of
// the Axiom API.
//
// Axiom API Reference: /api/v1/datasets
type DatasetsService service

// List all available datasets.
func (s *DatasetsService) List(ctx context.Context) ([]*Dataset, error) {
	ctx, span := s.client.trace(ctx, "Datasets.List")
	defer span.End()

	var res []*wrappedDataset
	if err := s.client.Call(ctx, http.MethodGet, s.basePath, nil, &res); err != nil {
		return nil, spanError(span, err)
	}

	datasets := make([]*Dataset, len(res))
	for i, r := range res {
		datasets[i] = &r.Dataset
	}

	return datasets, nil
}

// Get a dataset by id.
func (s *DatasetsService) Get(ctx context.Context, id string) (*Dataset, error) {
	ctx, span := s.client.trace(ctx, "Datasets.Get", trace.WithAttributes(
		attribute.String("axiom.dataset_id", id),
	))
	defer span.End()

	path := s.basePath + "/" + id

	var res wrappedDataset
	if err := s.client.Call(ctx, http.MethodGet, path, nil, &res); err != nil {
		return nil, spanError(span, err)
	}

	return &res.Dataset, nil
}

// Create a dataset with the given properties.
func (s *DatasetsService) Create(ctx context.Context, req DatasetCreateRequest) (*Dataset, error) {
	ctx, span := s.client.trace(ctx, "Datasets.Create", trace.WithAttributes(
		attribute.String("axiom.param.name", req.Name),
		attribute.String("axiom.param.description", req.Description),
	))
	defer span.End()

	var res wrappedDataset
	if err := s.client.Call(ctx, http.MethodPost, s.basePath, req, &res); err != nil {
		return nil, spanError(span, err)
	}

	return &res.Dataset, nil
}

// Update the dataset identified by the given id with the given properties.
func (s *DatasetsService) Update(ctx context.Context, id string, req DatasetUpdateRequest) (*Dataset, error) {
	ctx, span := s.client.trace(ctx, "Datasets.Update", trace.WithAttributes(
		attribute.String("axiom.dataset_id", id),
		attribute.String("axiom.param.description", req.Description),
	))
	defer span.End()

	path := s.basePath + "/" + id

	var res wrappedDataset
	if err := s.client.Call(ctx, http.MethodPut, path, req, &res); err != nil {
		return nil, spanError(span, err)
	}

	return &res.Dataset, nil
}

// Delete the dataset identified by the given id.
func (s *DatasetsService) Delete(ctx context.Context, id string) error {
	ctx, span := s.client.trace(ctx, "Datasets.Delete", trace.WithAttributes(
		attribute.String("axiom.dataset_id", id),
	))
	defer span.End()

	if err := s.client.Call(ctx, http.MethodDelete, s.basePath+"/"+id, nil, nil); err != nil {
		return spanError(span, err)
	}

	return nil
}

// Trim the dataset identified by its id to a given length. The max duration
// given will mark the oldest timestamp an event can have. Older ones will be
// deleted from the dataset.
func (s *DatasetsService) Trim(ctx context.Context, id string, maxDuration time.Duration) (*TrimResult, error) {
	ctx, span := s.client.trace(ctx, "Datasets.Trim", trace.WithAttributes(
		attribute.String("axiom.dataset_id", id),
		attribute.String("axiom.param.max_duration", maxDuration.String()),
	))
	defer span.End()

	req := datasetTrimRequest{
		MaxDuration: maxDuration.String(),
	}

	path := s.basePath + "/" + id + "/trim"

	var res TrimResult
	if err := s.client.Call(ctx, http.MethodPost, path, req, &res); err != nil {
		return nil, spanError(span, err)
	}

	return &res, nil
}

// Ingest data into the dataset identified by its id.
//
// Restrictions for field names (JSON object keys) can be reviewed here:
// https://www.axiom.co/docs/usage/field-restrictions.
func (s *DatasetsService) Ingest(ctx context.Context, id string, r io.Reader, typ ContentType, enc ContentEncoding, options ...ingest.Option) (*ingest.Status, error) {
	ctx, span := s.client.trace(ctx, "Datasets.Ingest", trace.WithAttributes(
		attribute.String("axiom.dataset_id", id),
		attribute.String("axiom.param.content_type", typ.String()),
		attribute.String("axiom.param.content_encoding", enc.String()),
	))
	defer span.End()

	// Apply supplied options.
	var opts ingest.Options
	for _, option := range options {
		option(&opts)
	}

	path, err := AddOptions(s.basePath+"/"+id+"/ingest", opts)
	if err != nil {
		return nil, spanError(span, err)
	}

	req, err := s.client.NewRequest(ctx, http.MethodPost, path, r)
	if err != nil {
		return nil, spanError(span, err)
	}

	switch typ {
	case JSON, NDJSON, CSV:
		req.Header.Set("Content-Type", typ.String())
	default:
		err = ErrUnknownContentType
		return nil, spanError(span, err)
	}

	switch enc {
	case Identity:
	case Gzip, Zstd:
		req.Header.Set("Content-Encoding", enc.String())
	default:
		err = ErrUnknownContentEncoding
		return nil, spanError(span, err)
	}

	var res ingest.Status
	if _, err = s.client.Do(req, &res); err != nil {
		return nil, spanError(span, err)
	}

	setIngestResultOnSpan(span, res)

	return &res, nil
}

// IngestEvents ingests events into the dataset identified by its id.
//
// Restrictions for field names (JSON object keys) can be reviewed here:
// https://www.axiom.co/docs/usage/field-restrictions.
func (s *DatasetsService) IngestEvents(ctx context.Context, id string, events []Event, options ...ingest.Option) (*ingest.Status, error) {
	ctx, span := s.client.trace(ctx, "Datasets.IngestEvents", trace.WithAttributes(
		attribute.String("axiom.dataset_id", id),
		attribute.Int("axiom.events_to_ingest", len(events)),
	))
	defer span.End()

	// Apply supplied options.
	var opts ingest.Options
	for _, option := range options {
		option(&opts)
	}

	if len(events) == 0 {
		return &ingest.Status{}, nil
	}

	path, err := AddOptions(s.basePath+"/"+id+"/ingest", opts)
	if err != nil {
		return nil, spanError(span, err)
	}

	pr, pw := io.Pipe()
	go func() {
		zsw, wErr := zstd.NewWriter(pw)
		if wErr != nil {
			_ = pw.CloseWithError(wErr)
			return
		}

		var (
			enc    = json.NewEncoder(zsw)
			encErr error
		)
		for _, event := range events {
			if encErr = enc.Encode(event); encErr != nil {
				break
			}
		}

		if closeErr := zsw.Close(); encErr == nil {
			// If we have no error from encoding but from closing, capture that
			// one.
			encErr = closeErr
		}
		_ = pw.CloseWithError(encErr)
	}()

	req, err := s.client.NewRequest(ctx, http.MethodPost, path, pr)
	if err != nil {
		return nil, spanError(span, err)
	}

	req.Header.Set("Content-Type", NDJSON.String())
	req.Header.Set("Content-Encoding", Zstd.String())

	var res ingest.Status
	if _, err = s.client.Do(req, &res); err != nil {
		return nil, spanError(span, err)
	}

	setIngestResultOnSpan(span, res)

	return &res, nil
}

// IngestChannel ingests events from a channel into the dataset identified by
// its id. As it keeps a connection open until the channel is closed, it is not
// advised to use this method for long-running ingestions.
//
// Restrictions for field names (JSON object keys) can be reviewed here:
// https://www.axiom.co/docs/usage/field-restrictions.
func (s *DatasetsService) IngestChannel(ctx context.Context, id string, events <-chan Event, options ...ingest.Option) (*ingest.Status, error) {
	ctx, span := s.client.trace(ctx, "Datasets.IngestChannel", trace.WithAttributes(
		attribute.String("axiom.dataset_id", id),
		attribute.Int("axiom.channel.capacity", cap(events)),
	))
	defer span.End()

	// Apply supplied options.
	var opts ingest.Options
	for _, option := range options {
		option(&opts)
	}

	path, err := AddOptions(s.basePath+"/"+id+"/ingest", opts)
	if err != nil {
		return nil, spanError(span, err)
	}

	pr, pw := io.Pipe()
	go func() {
		zsw, wErr := zstd.NewWriter(pw)
		if wErr != nil {
			_ = pw.CloseWithError(wErr)
			return
		}

		var (
			enc    = json.NewEncoder(zsw)
			encErr error
		)
		for event := range events {
			if encErr = enc.Encode(event); encErr != nil {
				break
			}
		}

		if closeErr := zsw.Close(); encErr == nil {
			// If we have no error from encoding but from closing, capture that
			// one.
			encErr = closeErr
		}
		_ = pw.CloseWithError(encErr)
	}()

	req, err := s.client.NewRequest(ctx, http.MethodPost, path, pr)
	if err != nil {
		return nil, spanError(span, err)
	}

	req.Header.Set("Content-Type", NDJSON.String())
	req.Header.Set("Content-Encoding", Zstd.String())

	var res ingest.Status
	if _, err = s.client.Do(req, &res); err != nil {
		return nil, spanError(span, err)
	}

	setIngestResultOnSpan(span, res)

	return &res, nil
}

// Query executes the given query specified using the Axiom Processing
// Language (APL).
func (s *DatasetsService) Query(ctx context.Context, q query.Query, options ...query.Option) (*query.Result, error) {
	// Apply supplied options.
	opts := struct {
		query.Options

		Format string `url:"format"`
	}{
		Format: "legacy", // Hardcode legacy APL format for now.
	}
	for _, option := range options {
		option(&opts.Options)
	}

	ctx, span := s.client.trace(ctx, "Datasets.Query", trace.WithAttributes(
		attribute.String("axiom.param.query", string(q)),
		attribute.String("axiom.param.start_time", opts.StartTime.String()),
		attribute.String("axiom.param.end_time", opts.EndTime.String()),
	))
	defer span.End()

	path, err := AddOptions(s.basePath+"/_apl", opts)
	if err != nil {
		return nil, spanError(span, err)
	}

	req, err := s.client.NewRequest(ctx, http.MethodPost, path, aplQueryRequest{
		Query:     string(q),
		StartTime: opts.StartTime,
		EndTime:   opts.EndTime,
	})
	if err != nil {
		return nil, spanError(span, err)
	}

	var (
		res struct {
			query.Result

			// HINT(lukasmalkmus): Ignore those fields as they are not relevant
			// for the user and will change with the new query result format.
			Request    any `json:"request"`
			Datasets   any `json:"datasetNames"`
			FieldsMeta any `json:"fieldsMetaMap"`
		}
		resp *Response
	)
	if resp, err = s.client.Do(req, &res); err != nil {
		return nil, spanError(span, err)
	}
	res.SavedQueryID = resp.Header.Get("X-Axiom-History-Query-Id")

	setQueryResultOnSpan(span, res.Result)

	return &res.Result, nil
}

// QueryLegacy executes the given legacy query on the dataset identified by its
// id.
//
// Deprecated: Legacy queries will be replaced by queries specified using the
// Axiom Processing Language (APL) and the legacy query API will be removed in
// the future. Use github.com/axiomhq/axiom-go/axiom/query instead.
func (s *DatasetsService) QueryLegacy(ctx context.Context, id string, q querylegacy.Query, opts querylegacy.Options) (*querylegacy.Result, error) {
	ctx, span := s.client.trace(ctx, "Datasets.QueryLegacy", trace.WithAttributes(
		attribute.String("axiom.dataset_id", id),
	))
	defer span.End()

	if opts.SaveKind == querylegacy.APL {
		err := fmt.Errorf("invalid query kind %q: must be %q or %q",
			opts.SaveKind, querylegacy.Analytics, querylegacy.Stream)
		return nil, spanError(span, err)
	}

	path, err := AddOptions(s.basePath+"/"+id+"/query", opts)
	if err != nil {
		return nil, spanError(span, err)
	}

	req, err := s.client.NewRequest(ctx, http.MethodPost, path, q)
	if err != nil {
		return nil, spanError(span, err)
	}

	var (
		res struct {
			querylegacy.Result

			// HINT(lukasmalkmus): Ignore those fields as they are not relevant
			// for the user.
			FieldsMeta any `json:"fieldsMeta"`
		}
		resp *Response
	)
	if resp, err = s.client.Do(req, &res); err != nil {
		return nil, spanError(span, err)
	}
	res.SavedQueryID = resp.Header.Get("X-Axiom-History-Query-Id")

	setQueryResultOnSpan(span, query.Result(res.Result))

	return &res.Result, nil
}

// DetectContentType detects the content type of an io.Reader's data. The
// returned io.Reader must be used instead of the passed one. Compressed content
// is not detected.
func DetectContentType(r io.Reader) (io.Reader, ContentType, error) {
	var (
		br  = bufio.NewReader(r)
		typ ContentType
	)
	for {
		var (
			c   rune
			err error
		)
		if c, _, err = br.ReadRune(); err == io.EOF {
			return nil, 0, errors.New("couldn't find beginning of supported ingestion format")
		} else if err != nil {
			return nil, 0, err
		} else if c == '[' {
			typ = JSON
		} else if c == '{' {
			typ = NDJSON
		} else if unicode.IsLetter(c) || c == '"' { // We assume a CSV table starts with a letter or a quote.
			typ = CSV
		} else if unicode.IsSpace(c) {
			continue
		} else {
			return nil, 0, errors.New("cannot determine content type")
		}

		if err = br.UnreadRune(); err != nil {
			return nil, 0, err
		}
		break
	}

	// Create a new reader and prepend what we have already consumed in order to
	// figure out the content type.
	buf, err := br.Peek(br.Buffered())
	if err != nil {
		return nil, 0, err
	}
	alreadyRead := bytes.NewReader(buf)
	r = io.MultiReader(alreadyRead, r)

	return r, typ, nil
}

func setIngestResultOnSpan(span trace.Span, res ingest.Status) {
	span.SetAttributes(
		attribute.Int64("axiom.events.ingested", int64(res.Ingested)),
		attribute.Int64("axiom.events.failed", int64(res.Failed)),
		attribute.Int64("axiom.events.processed_bytes", int64(res.ProcessedBytes)),
	)
}

func setQueryResultOnSpan(span trace.Span, res query.Result) {
	span.SetAttributes(
		attribute.Int64("axiom.result.matches", int64(res.Status.BlocksExamined)),
		attribute.String("axiom.result.status.elapsed_time", res.Status.ElapsedTime.String()),
		attribute.Int64("axiom.result.status.blocks_examined", int64(res.Status.BlocksExamined)),
		attribute.Int64("axiom.result.status.rows_examined", int64(res.Status.RowsExamined)),
		attribute.Int64("axiom.result.status.rows_matched", int64(res.Status.RowsMatched)),
		attribute.Int64("axiom.result.status.num_groups", int64(res.Status.NumGroups)),
		attribute.Bool("axiom.result.status.is_partial", res.Status.IsPartial),
		attribute.Bool("axiom.result.status.is_estimate", res.Status.IsEstimate),
		attribute.String("axiom.result.status.min_block_time", res.Status.MinBlockTime.String()),
		attribute.String("axiom.result.status.max_block_time", res.Status.MaxBlockTime.String()),
		attribute.String("axiom.result.status.min_cursor", res.Status.MinCursor),
		attribute.String("axiom.result.status.max_cursor", res.Status.MaxCursor),
	)
}
