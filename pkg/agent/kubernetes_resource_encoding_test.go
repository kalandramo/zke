package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// The Agent compresses a large list only for a Server that said it could
// decompress, and the bytes still decode to exactly the JSON it would have
// sent uncompressed.
func TestKubernetesResourceHandlerCompressesLargeListBodies(t *testing.T) {
	t.Parallel()

	objects := make([]runtime.Object, 0, 200)
	for index := range 200 {
		objects = append(objects, testUnstructuredNode(
			t,
			"node-"+string(rune('a'+index%26))+"-"+string(rune('a'+index/26)),
			map[string]string{"pool": "gpu"},
		))
	}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects...)
	handler := newKubernetesResourceHandler(client, nil, 32*1024*1024, 4096, "zke-system")

	list := func(accepted []agentv1.ContentEncoding) (*agentv1.ResourceResponse, []byte) {
		t.Helper()
		response, body, err := handler(
			context.Background(),
			&agentv1.ResourceRequest{
				Verb: agentv1.ResourceVerb_RESOURCE_VERB_LIST,
				Resource: &agentv1.GroupVersionResource{
					Version:  "v1",
					Resource: "nodes",
				},
				Representation:    agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
				ListOptions:       &agentv1.ListOptions{Limit: 500},
				AcceptedEncodings: accepted,
			},
			nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := io.ReadAll(body)
		if err != nil {
			t.Fatal(err)
		}
		if response.GetBodySize() != uint64(len(payload)) {
			t.Fatalf("declared %d wire bytes, sent %d", response.GetBodySize(), len(payload))
		}
		return response, payload
	}

	raw, rawBody := list(nil)
	if raw.GetContentEncoding() != agentv1.ContentEncoding_CONTENT_ENCODING_UNSPECIFIED ||
		raw.GetDecodedBodySize() != 0 {
		t.Fatalf("a Server that announced no encoding received %v", raw.GetContentEncoding())
	}

	compressed, wire := list(agentprotocol.AcceptedResourceEncodings())
	if compressed.GetContentEncoding() != agentv1.ContentEncoding_CONTENT_ENCODING_GZIP {
		t.Fatal("a large list was not compressed for a Server that accepts gzip")
	}
	if compressed.GetDecodedBodySize() != uint64(len(rawBody)) {
		t.Fatalf(
			"decoded size %d does not match the uncompressed body %d",
			compressed.GetDecodedBodySize(),
			len(rawBody),
		)
	}
	if compressed.GetBodySize() >= compressed.GetDecodedBodySize() {
		t.Fatal("compressed list is not smaller on the wire")
	}
	reader, err := gzip.NewReader(bytes.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, rawBody) {
		t.Fatal("compressed list does not decode to the uncompressed body")
	}
	var listed unstructured.UnstructuredList
	if err := listed.UnmarshalJSON(decoded); err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != len(objects) {
		t.Fatalf("decoded %d items, expected %d", len(listed.Items), len(objects))
	}
}
