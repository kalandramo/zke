package agentprotocol

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
)

const (
	// DefaultResourceCompressionThreshold is the smallest body an Agent will
	// compress. Below it the CPU and the extra round of allocation buy nothing:
	// a single Pod or a short list already fits in a handful of QUIC packets,
	// and compressing it only moves work from a link that was never the
	// bottleneck onto the Agent's own CPU.
	DefaultResourceCompressionThreshold uint64 = 64 * 1024
	// MaxAcceptedResourceEncodings bounds the encodings one request may list,
	// so a malformed request cannot make the reader walk a long list.
	MaxAcceptedResourceEncodings = 8
	// MaxResourceCompressionRatio bounds decoded size against wire size.
	//
	// The decoded ceiling is the real protection — nothing is ever decoded past
	// the negotiated body limit. This bounds the CPU instead: a body that
	// claims to expand a thousandfold is not a Kubernetes list, and refusing it
	// before the first byte is inflated keeps a hostile or broken Agent from
	// spending Server time rather than Server memory.
	MaxResourceCompressionRatio uint64 = 1000
)

// ErrResourceBodyEncoding is a body that does not decode: truncated, corrupt,
// or larger than it declared. It fails the one Stream carrying it.
var ErrResourceBodyEncoding = errors.New("invalid Resource body encoding")

// AcceptedResourceEncodings is what a reader that can inflate gzip announces.
//
// Identity is not listed: it is always acceptable, and an Agent that predates
// this field sends it whatever the request says.
func AcceptedResourceEncodings() []agentv1.ContentEncoding {
	return []agentv1.ContentEncoding{agentv1.ContentEncoding_CONTENT_ENCODING_GZIP}
}

// EncodedResourceBody is one Resource response body in the form it goes on the
// wire, together with what the response message must declare about it.
type EncodedResourceBody struct {
	Encoding agentv1.ContentEncoding
	Wire     []byte
	// DecodedSize is zero for an uncompressed body, matching the protocol:
	// there, Wire is already the decoded form and BodySize answers for both.
	DecodedSize uint64
}

// EncodeResourceBody chooses how one response body travels.
//
// It compresses only when the caller said it can decompress, the body is worth
// compressing, and compressing actually made it smaller. Anything else returns
// the body untouched, which is also what happens for a request from a Server
// that predates the field.
func EncodeResourceBody(
	request *agentv1.ResourceRequest,
	body []byte,
	threshold uint64,
) EncodedResourceBody {
	identity := EncodedResourceBody{
		Encoding: agentv1.ContentEncoding_CONTENT_ENCODING_UNSPECIFIED,
		Wire:     body,
	}
	if threshold == 0 {
		threshold = DefaultResourceCompressionThreshold
	}
	if uint64(len(body)) < threshold || !acceptsResourceEncoding(
		request,
		agentv1.ContentEncoding_CONTENT_ENCODING_GZIP,
	) {
		return identity
	}
	var compressed bytes.Buffer
	compressed.Grow(len(body) / 2)
	// BestSpeed on purpose. The point is to stop shipping raw JSON over a link
	// that may be a WAN, not to reach the smallest possible body: the higher
	// levels cost several times the CPU on the Agent for a few percent, and the
	// Agent shares that CPU with heartbeats and terminals.
	writer, err := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	if err != nil {
		return identity
	}
	if _, err := writer.Write(body); err != nil {
		return identity
	}
	if err := writer.Close(); err != nil {
		return identity
	}
	// A body that did not shrink — already-compressed bytes, or one small
	// enough that the gzip header dominates — travels as it was.
	if compressed.Len() >= len(body) {
		return identity
	}
	// Cannot happen for a body that shrank, but the ratio is part of the
	// contract the reader enforces, so it is not left to chance here.
	if uint64(len(body)) > MaxResourceCompressionRatio*uint64(compressed.Len()) {
		return identity
	}
	return EncodedResourceBody{
		Encoding:    agentv1.ContentEncoding_CONTENT_ENCODING_GZIP,
		Wire:        compressed.Bytes(),
		DecodedSize: uint64(len(body)),
	}
}

func acceptsResourceEncoding(
	request *agentv1.ResourceRequest,
	encoding agentv1.ContentEncoding,
) bool {
	for _, accepted := range request.GetAcceptedEncodings() {
		if accepted == encoding {
			return true
		}
	}
	return false
}

// ResourceBodyDecodedSize is how many bytes the body will occupy once read,
// which is what a reader budgets for.
func ResourceBodyDecodedSize(response *agentv1.ResourceResponse) uint64 {
	if response.GetContentEncoding() ==
		agentv1.ContentEncoding_CONTENT_ENCODING_UNSPECIFIED {
		return response.GetBodySize()
	}
	return response.GetDecodedBodySize()
}

// bodyEncoding is what a response says about the bytes that follow it: how many
// are on the wire, in what encoding, and how many they decode to.
type bodyEncoding struct {
	size        uint64
	encoding    agentv1.ContentEncoding
	decodedSize uint64
}

func readResourceBody(
	stream io.Reader,
	response *agentv1.ResourceResponse,
	responseBody io.Writer,
) error {
	return readEncodedBody(
		stream,
		bodyEncoding{
			size:        response.GetBodySize(),
			encoding:    response.GetContentEncoding(),
			decodedSize: response.GetDecodedBodySize(),
		},
		responseBody,
	)
}

// readEncodedBody copies one declared body from the Stream into the caller's
// writer, inflating it when it arrives compressed.
//
// Every failure here is confined to this Stream: the body is read through a
// limit that the declared wire size sets, the decoded output stops at the
// declared decoded size, and a body that does not end exactly where it said it
// would is refused rather than trusted.
func readEncodedBody(
	stream io.Reader,
	declared bodyEncoding,
	responseBody io.Writer,
) error {
	if declared.encoding ==
		agentv1.ContentEncoding_CONTENT_ENCODING_UNSPECIFIED {
		if _, err := io.CopyN(
			responseBody,
			stream,
			int64(declared.size),
		); err != nil {
			return fmt.Errorf("read Resource response body: %w", err)
		}
		return nil
	}
	wire := &io.LimitedReader{R: stream, N: int64(declared.size)}
	reader, err := gzip.NewReader(wire)
	if err != nil {
		return fmt.Errorf(
			"%w: open gzip Resource body: %w",
			ErrResourceBodyEncoding,
			err,
		)
	}
	defer reader.Close()
	if _, err := io.CopyN(
		responseBody,
		reader,
		int64(declared.decodedSize),
	); err != nil {
		return fmt.Errorf(
			"%w: inflate Resource body: %w",
			ErrResourceBodyEncoding,
			err,
		)
	}
	// The body declared its decoded size; anything past it means the
	// declaration was wrong, and a reader that kept going would be inflating an
	// unbounded body into a budget sized for the declaration.
	var trailing [1]byte
	if _, err := reader.Read(trailing[:]); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf(
				"%w: Resource body exceeds its declared decoded size",
				ErrResourceBodyEncoding,
			)
		}
		return fmt.Errorf(
			"%w: finish gzip Resource body: %w",
			ErrResourceBodyEncoding,
			err,
		)
	}
	if wire.N != 0 {
		return fmt.Errorf(
			"%w: Resource body is shorter than its declared wire size",
			ErrResourceBodyEncoding,
		)
	}
	return nil
}

func validateAcceptedEncodings(request *agentv1.ResourceRequest) error {
	return validateEncodings(request.GetAcceptedEncodings())
}

func validateEncodings(accepted []agentv1.ContentEncoding) error {
	if len(accepted) > MaxAcceptedResourceEncodings {
		return ErrStreamProtocol
	}
	seen := make(map[agentv1.ContentEncoding]struct{}, len(accepted))
	for _, encoding := range accepted {
		if encoding != agentv1.ContentEncoding_CONTENT_ENCODING_GZIP {
			return ErrStreamProtocol
		}
		if _, duplicate := seen[encoding]; duplicate {
			return ErrStreamProtocol
		}
		seen[encoding] = struct{}{}
	}
	return nil
}

func validateResourceBodyEncoding(
	response *agentv1.ResourceResponse,
	maxBodySize uint64,
) error {
	return validateBodyEncoding(
		bodyEncoding{
			size:        response.GetBodySize(),
			encoding:    response.GetContentEncoding(),
			decodedSize: response.GetDecodedBodySize(),
		},
		maxBodySize,
	)
}

func validateBodyEncoding(declared bodyEncoding, maxBodySize uint64) error {
	switch declared.encoding {
	case agentv1.ContentEncoding_CONTENT_ENCODING_UNSPECIFIED:
		if declared.decodedSize != 0 {
			return ErrStreamProtocol
		}
		return nil
	case agentv1.ContentEncoding_CONTENT_ENCODING_GZIP:
		if declared.size == 0 || declared.decodedSize == 0 {
			return ErrStreamProtocol
		}
		if declared.decodedSize > maxBodySize {
			return ErrStreamBodyTooLarge
		}
		if declared.decodedSize > MaxResourceCompressionRatio*declared.size {
			return ErrStreamBodyTooLarge
		}
		return nil
	default:
		return ErrStreamProtocol
	}
}

// EncodeBody is EncodeResourceBody for a caller that carries its accepted
// encodings itself rather than inside a ResourceRequest.
func EncodeBody(
	accepted []agentv1.ContentEncoding,
	body []byte,
	threshold uint64,
) EncodedResourceBody {
	return EncodeResourceBody(
		&agentv1.ResourceRequest{AcceptedEncodings: accepted},
		body,
		threshold,
	)
}
