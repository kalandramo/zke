package agentprotocol

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/quic-go/quic-go"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
)

const (
	// A cache answer is a page of compact summaries or a set of counters, not a
	// listing of whole objects, so it is bounded far below the generic Resource
	// body limit.
	DefaultMaxResourceCacheBodySize uint64 = 16 * 1024 * 1024
	// MaxResourceCacheLimit bounds one page. It matches the largest page the
	// Server's own list APIs accept, so a cached list cannot be asked for more
	// than an uncached one.
	MaxResourceCacheLimit     uint32 = 500
	maxResourceCachePageToken        = 4096
)

type ResourceCacheHandler func(
	context.Context,
	*agentv1.ResourceCacheRequest,
) (*agentv1.ResourceCacheResponse, io.Reader, error)

func ResourceCacheStreamHandler(
	maxBodySize uint64,
	handler ResourceCacheHandler,
) IncomingStreamHandler {
	if maxBodySize == 0 {
		maxBodySize = DefaultMaxResourceCacheBodySize
	}
	return func(
		ctx context.Context,
		stream *quic.Stream,
		header *agentv1.StreamHeader,
	) error {
		if handler == nil {
			return &StreamFailure{
				Code: StreamErrorUnsupported,
				Err:  ErrStreamUnsupported,
			}
		}
		request := &agentv1.ResourceCacheRequest{}
		if err := ReadMessage(stream, request); err != nil {
			return fmt.Errorf(
				"%w: read ResourceCacheRequest: %w",
				ErrStreamProtocol,
				err,
			)
		}
		if err := validateResourceCacheRequest(header, request); err != nil {
			return err
		}
		if err := requireStreamEOF(stream); err != nil {
			return err
		}
		response, responseBody, err := handler(ctx, request)
		if err != nil {
			return err
		}
		if err := validateResourceCacheResponse(
			response,
			responseBody != nil,
			maxBodySize,
		); err != nil {
			return err
		}
		if err := WriteMessage(stream, response); err != nil {
			return err
		}
		if response.GetBodySize() > 0 {
			if _, err := io.CopyN(
				stream,
				responseBody,
				int64(response.GetBodySize()),
			); err != nil {
				return fmt.Errorf("write Resource cache response body: %w", err)
			}
		}
		return nil
	}
}

func DoResourceCache(
	ctx context.Context,
	connection *quic.Conn,
	header *agentv1.StreamHeader,
	request *agentv1.ResourceCacheRequest,
	responseBody io.Writer,
	maxBodySize uint64,
) (*agentv1.ResourceCacheResponse, error) {
	if connection == nil {
		return nil, errors.New("Agent Connection is required")
	}
	if maxBodySize == 0 {
		maxBodySize = DefaultMaxResourceCacheBodySize
	}
	if err := validateStreamHeader(header); err != nil {
		return nil, err
	}
	if header.GetKind() != agentv1.StreamKind_STREAM_KIND_RESOURCE_CACHE {
		return nil, ErrStreamProtocol
	}
	if err := validateResourceCacheRequest(header, request); err != nil {
		return nil, err
	}

	stream, err := connection.OpenStreamSync(ctx)
	if err != nil {
		return nil, fmt.Errorf("open Agent Resource cache Stream: %w", err)
	}
	finished := false
	defer func() {
		if !finished {
			AbortStream(stream, StreamErrorCanceled)
		}
	}()
	stopCancellation := context.AfterFunc(ctx, func() {
		AbortStream(stream, streamErrorCode(ctx.Err(), ctx))
	})
	defer stopCancellation()

	if deadline, ok := ctx.Deadline(); ok {
		if err := stream.SetDeadline(deadline); err != nil {
			return nil, err
		}
	} else {
		deadline := time.Now().Add(
			time.Duration(header.GetTimeoutMillis()) * time.Millisecond,
		)
		if err := stream.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}
	if err := WriteMessage(stream, header); err != nil {
		return nil, err
	}
	if err := WriteMessage(stream, request); err != nil {
		return nil, err
	}
	if err := stream.Close(); err != nil {
		return nil, fmt.Errorf("finish Agent Resource cache request: %w", err)
	}

	response := &agentv1.ResourceCacheResponse{}
	if err := ReadMessage(stream, response); err != nil {
		return nil, err
	}
	if err := validateResourceCacheResponse(
		response,
		responseBody != nil,
		maxBodySize,
	); err != nil {
		return nil, err
	}
	if response.GetBodySize() > 0 {
		if reserver, ok := responseBody.(ResourceBodyReserver); ok {
			if err := reserver.ReserveResourceBody(
				resourceCacheDecodedSize(response),
			); err != nil {
				return nil, err
			}
		}
		if err := readEncodedBody(
			stream,
			bodyEncoding{
				size:        response.GetBodySize(),
				encoding:    response.GetContentEncoding(),
				decodedSize: response.GetDecodedBodySize(),
			},
			responseBody,
		); err != nil {
			return nil, err
		}
	}
	if err := requireStreamEOF(stream); err != nil {
		return nil, err
	}
	finished = true
	return response, nil
}

// AcceptedResourceCacheEncodings mirrors AcceptedResourceEncodings: a summary
// page is JSON like any other body, and the same compression applies to it.
func AcceptedResourceCacheEncodings() []agentv1.ContentEncoding {
	return AcceptedResourceEncodings()
}

func resourceCacheDecodedSize(response *agentv1.ResourceCacheResponse) uint64 {
	if response.GetContentEncoding() ==
		agentv1.ContentEncoding_CONTENT_ENCODING_UNSPECIFIED {
		return response.GetBodySize()
	}
	return response.GetDecodedBodySize()
}

func validateResourceCacheRequest(
	header *agentv1.StreamHeader,
	request *agentv1.ResourceCacheRequest,
) error {
	if request == nil || header.GetIdempotencyKey() != "" {
		return ErrStreamProtocol
	}
	if err := validateEncodings(request.GetAcceptedEncodings()); err != nil {
		return err
	}
	if len(request.GetLabelSelector()) > maxResourceStringLength ||
		len(request.GetFieldSelector()) > maxResourceStringLength ||
		len(request.GetPageToken()) > maxResourceCachePageToken ||
		strings.TrimSpace(request.GetPageToken()) != request.GetPageToken() ||
		request.GetLimit() > MaxResourceCacheLimit {
		return ErrStreamProtocol
	}
	switch request.GetQuery() {
	case agentv1.ResourceCacheQuery_RESOURCE_CACHE_QUERY_OVERVIEW_SNAPSHOT:
		// An overview is the whole Cluster by definition. A request that also
		// names a resource, a Namespace or a page is describing something else,
		// and is refused rather than silently reinterpreted.
		if request.GetResource() != nil ||
			request.GetNamespace() != "" ||
			request.GetLabelSelector() != "" ||
			request.GetFieldSelector() != "" ||
			request.GetLimit() != 0 ||
			request.GetPageToken() != "" {
			return ErrStreamProtocol
		}
		return nil
	case agentv1.ResourceCacheQuery_RESOURCE_CACHE_QUERY_SUMMARY_LIST:
		resource := request.GetResource()
		if resource == nil ||
			(resource.GetGroup() != "" && !validResourceSegment(resource.GetGroup())) ||
			!validResourceSegment(resource.GetVersion()) ||
			!validResourceSegment(resource.GetResource()) ||
			(request.GetNamespace() != "" &&
				!validResourceSegment(request.GetNamespace())) {
			return ErrStreamProtocol
		}
		return nil
	default:
		return ErrStreamProtocol
	}
}

func validateResourceCacheResponse(
	response *agentv1.ResourceCacheResponse,
	hasBody bool,
	maxBodySize uint64,
) error {
	if response == nil ||
		response.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED ||
		len(response.GetReason()) > maxResourceStringLength ||
		len(response.GetMessage()) > maxResourceStringLength ||
		len(response.GetContentType()) > 256 ||
		response.GetBodySize() > maxBodySize {
		if response != nil && response.GetBodySize() > maxBodySize {
			return ErrStreamBodyTooLarge
		}
		return ErrStreamProtocol
	}
	if response.GetBodySize() > 0 && !hasBody {
		return ErrStreamProtocol
	}
	return validateBodyEncoding(
		bodyEncoding{
			size:        response.GetBodySize(),
			encoding:    response.GetContentEncoding(),
			decodedSize: response.GetDecodedBodySize(),
		},
		maxBodySize,
	)
}
