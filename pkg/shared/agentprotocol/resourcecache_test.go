package agentprotocol

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
)

func cacheHeader(requestID string) *agentv1.StreamHeader {
	return &agentv1.StreamHeader{
		ProtocolVersion: ProtocolVersion,
		Kind:            agentv1.StreamKind_STREAM_KIND_RESOURCE_CACHE,
		RequestId:       requestID,
		TimeoutMillis:   5000,
	}
}

func TestValidateResourceCacheRequest(t *testing.T) {
	t.Parallel()
	header := cacheHeader("00000000-0000-4000-8000-000000000001")
	cases := []struct {
		name    string
		request *agentv1.ResourceCacheRequest
		wantErr bool
	}{
		{
			name: "an overview names nothing else",
			request: &agentv1.ResourceCacheRequest{
				Query:             agentv1.ResourceCacheQuery_RESOURCE_CACHE_QUERY_OVERVIEW_SNAPSHOT,
				AcceptedEncodings: AcceptedResourceCacheEncodings(),
			},
		},
		{
			name: "an overview that names a resource is refused",
			request: &agentv1.ResourceCacheRequest{
				Query: agentv1.ResourceCacheQuery_RESOURCE_CACHE_QUERY_OVERVIEW_SNAPSHOT,
				Resource: &agentv1.GroupVersionResource{
					Version: "v1", Resource: "pods",
				},
			},
			wantErr: true,
		},
		{
			name: "an overview that names a Namespace is refused",
			request: &agentv1.ResourceCacheRequest{
				Query:     agentv1.ResourceCacheQuery_RESOURCE_CACHE_QUERY_OVERVIEW_SNAPSHOT,
				Namespace: "default",
			},
			wantErr: true,
		},
		{
			name: "a summary list names its resource",
			request: &agentv1.ResourceCacheRequest{
				Query: agentv1.ResourceCacheQuery_RESOURCE_CACHE_QUERY_SUMMARY_LIST,
				Resource: &agentv1.GroupVersionResource{
					Version: "v1", Resource: "pods",
				},
				Namespace: "default",
				Limit:     100,
			},
		},
		{
			name: "a summary list without a resource is refused",
			request: &agentv1.ResourceCacheRequest{
				Query: agentv1.ResourceCacheQuery_RESOURCE_CACHE_QUERY_SUMMARY_LIST,
			},
			wantErr: true,
		},
		{
			name: "a page larger than the ceiling is refused",
			request: &agentv1.ResourceCacheRequest{
				Query: agentv1.ResourceCacheQuery_RESOURCE_CACHE_QUERY_SUMMARY_LIST,
				Resource: &agentv1.GroupVersionResource{
					Version: "v1", Resource: "pods",
				},
				Limit: uint32(MaxResourceCacheLimit) + 1,
			},
			wantErr: true,
		},
		{
			name:    "an unspecified query is refused",
			request: &agentv1.ResourceCacheRequest{},
			wantErr: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateResourceCacheRequest(header, testCase.request)
			if testCase.wantErr != (err != nil) {
				t.Fatalf("got %v, want error: %v", err, testCase.wantErr)
			}
		})
	}
}

// A cache query is a read. Carrying an idempotency key would say it was
// something that could be replayed, and it is refused rather than ignored.
func TestResourceCacheRequestRejectsAnIdempotencyKey(t *testing.T) {
	t.Parallel()
	header := cacheHeader("00000000-0000-4000-8000-000000000002")
	header.IdempotencyKey = "0123456789abcdef0123"
	err := validateResourceCacheRequest(header, &agentv1.ResourceCacheRequest{
		Query: agentv1.ResourceCacheQuery_RESOURCE_CACHE_QUERY_OVERVIEW_SNAPSHOT,
	})
	if !errors.Is(err, ErrStreamProtocol) {
		t.Fatalf("got %v", err)
	}
}

// The whole round trip over a real QUIC connection, compressed and not, plus a
// body that does not match what it declared — which must cost that Stream and
// leave the Connection working.
func TestResourceCacheOverQUIC(t *testing.T) {
	snapshot := compressibleBody(int(DefaultResourceCompressionThreshold) * 2)
	client, server, stop := openStreamTestConnection(t)
	defer stop()

	handle := func(
		_ context.Context,
		request *agentv1.ResourceCacheRequest,
	) (*agentv1.ResourceCacheResponse, io.Reader, error) {
		if request.GetQuery() ==
			agentv1.ResourceCacheQuery_RESOURCE_CACHE_QUERY_SUMMARY_LIST {
			return &agentv1.ResourceCacheResponse{
				Result:  agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
				Reason:  "UnsupportedResourceCacheQuery",
				Message: "requested Agent resource cache query is unsupported",
			}, nil, nil
		}
		encoded := EncodeBody(request.GetAcceptedEncodings(), snapshot, 0)
		return &agentv1.ResourceCacheResponse{
			Result:          agentv1.ResultCode_RESULT_CODE_OK,
			ContentType:     "application/json",
			BodySize:        uint64(len(encoded.Wire)),
			ContentEncoding: encoded.Encoding,
			DecodedBodySize: encoded.DecodedSize,
		}, bytes.NewReader(encoded.Wire), nil
	}

	streamServer, err := NewStreamServer(StreamServerConfig{
		HeaderTimeout: time.Second,
		MaxTimeout:    5 * time.Second,
		Handlers: map[agentv1.StreamKind]StreamHandlerConfig{
			agentv1.StreamKind_STREAM_KIND_RESOURCE_CACHE: {
				MaxConcurrent: 4,
				MaxTimeout:    5 * time.Second,
				Handle:        ResourceCacheStreamHandler(8*1024*1024, handle),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	serveContext, cancelServe := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- streamServer.Serve(serveContext, server) }()
	defer func() {
		cancelServe()
		_ = client.CloseWithError(0, "test complete")
		<-serveDone
	}()

	ask := func(
		t *testing.T,
		requestID string,
		request *agentv1.ResourceCacheRequest,
	) (*agentv1.ResourceCacheResponse, *bytes.Buffer, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		received := &bytes.Buffer{}
		response, err := DoResourceCache(
			ctx,
			client,
			cacheHeader(requestID),
			request,
			received,
			8*1024*1024,
		)
		return response, received, err
	}

	t.Run("a compressed snapshot round trips", func(t *testing.T) {
		response, received, err := ask(
			t,
			"00000000-0000-4000-8000-000000000011",
			&agentv1.ResourceCacheRequest{
				Query:             agentv1.ResourceCacheQuery_RESOURCE_CACHE_QUERY_OVERVIEW_SNAPSHOT,
				AcceptedEncodings: AcceptedResourceCacheEncodings(),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if response.GetContentEncoding() != agentv1.ContentEncoding_CONTENT_ENCODING_GZIP {
			t.Fatal("a large snapshot was not compressed")
		}
		if !bytes.Equal(received.Bytes(), snapshot) {
			t.Fatal("the snapshot did not survive the round trip")
		}
	})

	t.Run("a Server that announces nothing gets raw JSON", func(t *testing.T) {
		response, received, err := ask(
			t,
			"00000000-0000-4000-8000-000000000012",
			&agentv1.ResourceCacheRequest{
				Query: agentv1.ResourceCacheQuery_RESOURCE_CACHE_QUERY_OVERVIEW_SNAPSHOT,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if response.GetContentEncoding() != agentv1.ContentEncoding_CONTENT_ENCODING_UNSPECIFIED {
			t.Fatal("an unannounced encoding was used anyway")
		}
		if !bytes.Equal(received.Bytes(), snapshot) {
			t.Fatal("the snapshot did not survive the round trip")
		}
	})

	t.Run("an unsupported query is a response, not a broken Stream", func(t *testing.T) {
		response, _, err := ask(
			t,
			"00000000-0000-4000-8000-000000000013",
			&agentv1.ResourceCacheRequest{
				Query: agentv1.ResourceCacheQuery_RESOURCE_CACHE_QUERY_SUMMARY_LIST,
				Resource: &agentv1.GroupVersionResource{
					Version: "v1", Resource: "pods",
				},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if response.GetResult() != agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT {
			t.Fatalf("result = %v", response.GetResult())
		}
		// The Connection is still good.
		if _, _, err := ask(
			t,
			"00000000-0000-4000-8000-000000000014",
			&agentv1.ResourceCacheRequest{
				Query: agentv1.ResourceCacheQuery_RESOURCE_CACHE_QUERY_OVERVIEW_SNAPSHOT,
			},
		); err != nil {
			t.Fatalf("the Connection did not survive a refused query: %v", err)
		}
	})
}
