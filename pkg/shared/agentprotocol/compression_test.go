package agentprotocol

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
)

func compressibleBody(size int) []byte {
	body := make([]byte, 0, size+64)
	for len(body) < size {
		body = append(body, `{"kind":"Pod","metadata":{"name":"workload-`...)
		body = append(body, `0000000000","namespace":"default"}},`...)
	}
	return body
}

func gzipBytes(t *testing.T, body []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func gzipRequest() *agentv1.ResourceRequest {
	return &agentv1.ResourceRequest{
		AcceptedEncodings: AcceptedResourceEncodings(),
	}
}

// An Agent only compresses when the caller said it could decompress, which is
// what keeps a Server that predates the field from receiving a body it cannot
// read.
func TestEncodeResourceBodyRequiresAnAcceptedEncoding(t *testing.T) {
	body := compressibleBody(int(DefaultResourceCompressionThreshold) * 2)

	legacy := EncodeResourceBody(&agentv1.ResourceRequest{}, body, 0)
	if legacy.Encoding != agentv1.ContentEncoding_CONTENT_ENCODING_UNSPECIFIED ||
		!bytes.Equal(legacy.Wire, body) ||
		legacy.DecodedSize != 0 {
		t.Fatalf("body was encoded for a caller that accepted nothing: %v", legacy.Encoding)
	}

	current := EncodeResourceBody(gzipRequest(), body, 0)
	if current.Encoding != agentv1.ContentEncoding_CONTENT_ENCODING_GZIP {
		t.Fatal("body was not compressed for a caller that accepts gzip")
	}
	if current.DecodedSize != uint64(len(body)) {
		t.Fatalf("decoded size %d does not describe the body %d", current.DecodedSize, len(body))
	}
	if uint64(len(current.Wire)) >= current.DecodedSize {
		t.Fatal("compressed body is not smaller than the original")
	}
}

func TestEncodeResourceBodyLeavesSmallBodiesAlone(t *testing.T) {
	body := compressibleBody(1024)
	encoded := EncodeResourceBody(gzipRequest(), body, 0)
	if encoded.Encoding != agentv1.ContentEncoding_CONTENT_ENCODING_UNSPECIFIED {
		t.Fatal("a body below the threshold was compressed")
	}

	lowered := EncodeResourceBody(gzipRequest(), body, 512)
	if lowered.Encoding != agentv1.ContentEncoding_CONTENT_ENCODING_GZIP {
		t.Fatal("a configured threshold below the body size did not take effect")
	}
}

// Random bytes do not compress; sending them through gzip anyway would grow
// the body and spend CPU for it.
func TestEncodeResourceBodyKeepsIncompressibleBodies(t *testing.T) {
	body := make([]byte, DefaultResourceCompressionThreshold*2)
	state := uint64(0x9E3779B97F4A7C15)
	for index := range body {
		state = state*6364136223846793005 + 1442695040888963407
		body[index] = byte(state >> 33)
	}
	encoded := EncodeResourceBody(gzipRequest(), body, 0)
	if encoded.Encoding != agentv1.ContentEncoding_CONTENT_ENCODING_UNSPECIFIED {
		t.Fatal("an incompressible body was compressed")
	}
}

func TestReadResourceBodyRoundTrip(t *testing.T) {
	body := compressibleBody(int(DefaultResourceCompressionThreshold) * 2)
	encoded := EncodeResourceBody(gzipRequest(), body, 0)
	response := &agentv1.ResourceResponse{
		Result:          agentv1.ResultCode_RESULT_CODE_OK,
		BodySize:        uint64(len(encoded.Wire)),
		ContentEncoding: encoded.Encoding,
		DecodedBodySize: encoded.DecodedSize,
	}
	var decoded bytes.Buffer
	if err := readResourceBody(bytes.NewReader(encoded.Wire), response, &decoded); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded.Bytes(), body) {
		t.Fatal("decoded body does not match the original")
	}
	if ResourceBodyDecodedSize(response) != uint64(len(body)) {
		t.Fatal("decoded size does not describe what the reader will hold")
	}
}

func TestReadResourceBodyRejectsTruncationAndOverrun(t *testing.T) {
	body := compressibleBody(int(DefaultResourceCompressionThreshold) * 2)
	wire := gzipBytes(t, body)

	truncated := &agentv1.ResourceResponse{
		Result:          agentv1.ResultCode_RESULT_CODE_OK,
		BodySize:        uint64(len(wire)),
		ContentEncoding: agentv1.ContentEncoding_CONTENT_ENCODING_GZIP,
		DecodedBodySize: uint64(len(body)),
	}
	var discarded bytes.Buffer
	err := readResourceBody(bytes.NewReader(wire[:len(wire)/2]), truncated, &discarded)
	if !errors.Is(err, ErrResourceBodyEncoding) {
		t.Fatalf("a truncated body was accepted: %v", err)
	}

	// The body decodes to more than it declared: the reader must stop rather
	// than inflate past the space its caller reserved.
	understated := &agentv1.ResourceResponse{
		Result:          agentv1.ResultCode_RESULT_CODE_OK,
		BodySize:        uint64(len(wire)),
		ContentEncoding: agentv1.ContentEncoding_CONTENT_ENCODING_GZIP,
		DecodedBodySize: uint64(len(body)) - 16,
	}
	discarded.Reset()
	err = readResourceBody(bytes.NewReader(wire), understated, &discarded)
	if !errors.Is(err, ErrResourceBodyEncoding) {
		t.Fatalf("a body larger than it declared was accepted: %v", err)
	}
	if uint64(discarded.Len()) > understated.GetDecodedBodySize() {
		t.Fatalf("reader wrote %d bytes past the declared size", discarded.Len())
	}

	corrupt := append([]byte(nil), wire...)
	corrupt[len(corrupt)/2] ^= 0xFF
	broken := &agentv1.ResourceResponse{
		Result:          agentv1.ResultCode_RESULT_CODE_OK,
		BodySize:        uint64(len(corrupt)),
		ContentEncoding: agentv1.ContentEncoding_CONTENT_ENCODING_GZIP,
		DecodedBodySize: uint64(len(body)),
	}
	discarded.Reset()
	if err := readResourceBody(
		bytes.NewReader(corrupt),
		broken,
		&discarded,
	); !errors.Is(err, ErrResourceBodyEncoding) {
		t.Fatalf("a corrupt body was accepted: %v", err)
	}
}

// Decompression follows the Stream, so a canceled request stops inflating
// instead of running a large body to completion on a Server nobody is waiting
// on.
func TestReadResourceBodyStopsWithTheStream(t *testing.T) {
	body := compressibleBody(4 * 1024 * 1024)
	wire := gzipBytes(t, body)
	reader, writer := io.Pipe()
	go func() {
		_, _ = writer.Write(wire[:64])
		_ = writer.CloseWithError(context.Canceled)
	}()
	response := &agentv1.ResourceResponse{
		Result:          agentv1.ResultCode_RESULT_CODE_OK,
		BodySize:        uint64(len(wire)),
		ContentEncoding: agentv1.ContentEncoding_CONTENT_ENCODING_GZIP,
		DecodedBodySize: uint64(len(body)),
	}
	var decoded bytes.Buffer
	err := readResourceBody(reader, response, &decoded)
	if err == nil {
		t.Fatal("a canceled body read reported success")
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, ErrResourceBodyEncoding) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateResourceResponseBodyEncoding(t *testing.T) {
	const maxBodySize = 1024 * 1024
	cases := []struct {
		name     string
		response *agentv1.ResourceResponse
		want     error
	}{
		{
			name: "raw body carries no decoded size",
			response: &agentv1.ResourceResponse{
				Result:          agentv1.ResultCode_RESULT_CODE_OK,
				BodySize:        16,
				DecodedBodySize: 32,
			},
			want: ErrStreamProtocol,
		},
		{
			name: "gzip body declares its decoded size",
			response: &agentv1.ResourceResponse{
				Result:          agentv1.ResultCode_RESULT_CODE_OK,
				BodySize:        16,
				ContentEncoding: agentv1.ContentEncoding_CONTENT_ENCODING_GZIP,
			},
			want: ErrStreamProtocol,
		},
		{
			name: "decoded size stays under the negotiated limit",
			response: &agentv1.ResourceResponse{
				Result:          agentv1.ResultCode_RESULT_CODE_OK,
				BodySize:        4096,
				ContentEncoding: agentv1.ContentEncoding_CONTENT_ENCODING_GZIP,
				DecodedBodySize: maxBodySize + 1,
			},
			want: ErrStreamBodyTooLarge,
		},
		{
			name: "compression ratio is bounded",
			response: &agentv1.ResourceResponse{
				Result:          agentv1.ResultCode_RESULT_CODE_OK,
				BodySize:        16,
				ContentEncoding: agentv1.ContentEncoding_CONTENT_ENCODING_GZIP,
				DecodedBodySize: 16*MaxResourceCompressionRatio + 1,
			},
			want: ErrStreamBodyTooLarge,
		},
		{
			name: "unknown encoding",
			response: &agentv1.ResourceResponse{
				Result:          agentv1.ResultCode_RESULT_CODE_OK,
				BodySize:        16,
				ContentEncoding: agentv1.ContentEncoding(99),
				DecodedBodySize: 32,
			},
			want: ErrStreamProtocol,
		},
		{
			name: "well formed gzip body",
			response: &agentv1.ResourceResponse{
				Result:          agentv1.ResultCode_RESULT_CODE_OK,
				BodySize:        4096,
				ContentEncoding: agentv1.ContentEncoding_CONTENT_ENCODING_GZIP,
				DecodedBodySize: 65536,
			},
			want: nil,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateResourceResponse(testCase.response, true, maxBodySize)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("got %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestValidateAcceptedEncodings(t *testing.T) {
	base := func(encodings ...agentv1.ContentEncoding) *agentv1.ResourceRequest {
		return &agentv1.ResourceRequest{
			Verb: agentv1.ResourceVerb_RESOURCE_VERB_LIST,
			Resource: &agentv1.GroupVersionResource{
				Version: "v1", Resource: "pods",
			},
			Representation:    agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
			AcceptedEncodings: encodings,
		}
	}
	header := &agentv1.StreamHeader{
		ProtocolVersion: ProtocolVersion,
		Kind:            agentv1.StreamKind_STREAM_KIND_RESOURCE,
		RequestId:       "00000000-0000-4000-8000-000000000001",
		TimeoutMillis:   1000,
	}
	if err := validateResourceRequest(
		header,
		base(agentv1.ContentEncoding_CONTENT_ENCODING_GZIP),
		1024,
	); err != nil {
		t.Fatalf("a gzip-accepting request was refused: %v", err)
	}
	if err := validateResourceRequest(header, base(), 1024); err != nil {
		t.Fatalf("a request from a Server predating the field was refused: %v", err)
	}
	duplicated := base(
		agentv1.ContentEncoding_CONTENT_ENCODING_GZIP,
		agentv1.ContentEncoding_CONTENT_ENCODING_GZIP,
	)
	if err := validateResourceRequest(header, duplicated, 1024); !errors.Is(err, ErrStreamProtocol) {
		t.Fatalf("a duplicated encoding was accepted: %v", err)
	}
	unknown := base(agentv1.ContentEncoding(42))
	if err := validateResourceRequest(header, unknown, 1024); !errors.Is(err, ErrStreamProtocol) {
		t.Fatalf("an unknown encoding was accepted: %v", err)
	}
}

// Both directions of a rolling upgrade over a real QUIC connection: an Agent
// that compresses, an Agent that does not, and an Agent whose body does not
// match what it declared — which must cost that one Stream and nothing else.
func TestResourceBodyCompressionOverQUIC(t *testing.T) {
	body := compressibleBody(int(DefaultResourceCompressionThreshold) * 4)
	client, server, stop := openStreamTestConnection(t)
	defer stop()

	handle := func(
		_ context.Context,
		request *agentv1.ResourceRequest,
		_ io.Reader,
	) (*agentv1.ResourceResponse, io.Reader, error) {
		response := &agentv1.ResourceResponse{
			Result:               agentv1.ResultCode_RESULT_CODE_OK,
			KubernetesStatusCode: 200,
			ContentType:          "application/json",
		}
		if request.GetName() == "lying-agent" {
			wire := gzipBytes(t, body)
			response.BodySize = uint64(len(wire))
			response.ContentEncoding = agentv1.ContentEncoding_CONTENT_ENCODING_GZIP
			// Half the truth: the Stream must fail rather than inflate the rest.
			response.DecodedBodySize = uint64(len(body) / 2)
			return response, bytes.NewReader(wire), nil
		}
		threshold := DefaultResourceCompressionThreshold
		if request.GetName() == "legacy-agent" {
			// Stands in for an Agent that predates the field: it never looks at
			// accepted_encodings, so it always answers in plain JSON.
			request = &agentv1.ResourceRequest{}
		}
		encoded := EncodeResourceBody(request, body, threshold)
		response.BodySize = uint64(len(encoded.Wire))
		response.ContentEncoding = encoded.Encoding
		response.DecodedBodySize = encoded.DecodedSize
		return response, bytes.NewReader(encoded.Wire), nil
	}

	streamServer, err := NewStreamServer(StreamServerConfig{
		HeaderTimeout: time.Second,
		MaxTimeout:    5 * time.Second,
		Handlers: map[agentv1.StreamKind]StreamHandlerConfig{
			agentv1.StreamKind_STREAM_KIND_RESOURCE: {
				MaxConcurrent: 4,
				MaxTimeout:    5 * time.Second,
				Handle:        ResourceStreamHandler(8*1024*1024, handle),
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

	requestIDs := map[string]string{
		"current-agent": "00000000-0000-4000-8000-000000000001",
		"legacy-agent":  "00000000-0000-4000-8000-000000000002",
		"lying-agent":   "00000000-0000-4000-8000-000000000003",
	}
	get := func(t *testing.T, name string, accepted []agentv1.ContentEncoding) (
		*agentv1.ResourceResponse,
		*bytes.Buffer,
		error,
	) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		received := &bytes.Buffer{}
		response, err := DoResource(
			ctx,
			client,
			&agentv1.StreamHeader{
				ProtocolVersion: ProtocolVersion,
				Kind:            agentv1.StreamKind_STREAM_KIND_RESOURCE,
				RequestId:       requestIDs[name],
				TimeoutMillis:   5000,
			},
			&agentv1.ResourceRequest{
				Verb: agentv1.ResourceVerb_RESOURCE_VERB_GET,
				Resource: &agentv1.GroupVersionResource{
					Version: "v1", Resource: "pods",
				},
				Namespace:         "default",
				Name:              name,
				Representation:    agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
				AcceptedEncodings: accepted,
			},
			nil,
			received,
			8*1024*1024,
		)
		return response, received, err
	}

	t.Run("new Server and new Agent use gzip", func(t *testing.T) {
		response, received, err := get(t, "current-agent", AcceptedResourceEncodings())
		if err != nil {
			t.Fatal(err)
		}
		if response.GetContentEncoding() != agentv1.ContentEncoding_CONTENT_ENCODING_GZIP {
			t.Fatal("body was not compressed")
		}
		if response.GetBodySize() >= response.GetDecodedBodySize() {
			t.Fatal("wire size is not smaller than the decoded size")
		}
		if !bytes.Equal(received.Bytes(), body) {
			t.Fatal("decoded body does not match")
		}
	})

	t.Run("new Server and old Agent use raw JSON", func(t *testing.T) {
		response, received, err := get(t, "legacy-agent", AcceptedResourceEncodings())
		if err != nil {
			t.Fatal(err)
		}
		if response.GetContentEncoding() != agentv1.ContentEncoding_CONTENT_ENCODING_UNSPECIFIED {
			t.Fatal("an Agent predating the field returned a compressed body")
		}
		if !bytes.Equal(received.Bytes(), body) {
			t.Fatal("body does not match")
		}
	})

	t.Run("old Server and new Agent use raw JSON", func(t *testing.T) {
		response, received, err := get(t, "current-agent", nil)
		if err != nil {
			t.Fatal(err)
		}
		if response.GetContentEncoding() != agentv1.ContentEncoding_CONTENT_ENCODING_UNSPECIFIED {
			t.Fatal("a Server that announced nothing received a compressed body")
		}
		if !bytes.Equal(received.Bytes(), body) {
			t.Fatal("body does not match")
		}
	})

	t.Run("a body that lies fails only its own Stream", func(t *testing.T) {
		_, _, err := get(t, "lying-agent", AcceptedResourceEncodings())
		if err == nil {
			t.Fatal("a body larger than it declared was accepted")
		}
		if !strings.Contains(err.Error(), ErrResourceBodyEncoding.Error()) {
			t.Fatalf("unexpected error: %v", err)
		}
		// The Connection is still usable, which is the point.
		_, received, err := get(t, "current-agent", AcceptedResourceEncodings())
		if err != nil {
			t.Fatalf("Connection did not survive one failed body: %v", err)
		}
		if !bytes.Equal(received.Bytes(), body) {
			t.Fatal("body does not match after a failed Stream")
		}
	})
}
