package agentprotocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
)

// kubernetesListBody is a body shaped like what the Agent actually returns: a
// list of Kubernetes objects, repetitive in structure and varied in identity.
// Compression measured on uniform filler would flatter gzip; measured on random
// bytes it would libel it.
func kubernetesListBody(items int) []byte {
	type object struct {
		APIVersion string            `json:"apiVersion"`
		Kind       string            `json:"kind"`
		Metadata   map[string]any    `json:"metadata"`
		Spec       map[string]any    `json:"spec"`
		Status     map[string]any    `json:"status"`
		Labels     map[string]string `json:"labels"`
	}
	list := make([]object, 0, items)
	for index := range items {
		list = append(list, object{
			APIVersion: "v1",
			Kind:       "Pod",
			Metadata: map[string]any{
				"name":            fmt.Sprintf("workload-%06d", index),
				"namespace":       fmt.Sprintf("team-%02d", index%20),
				"uid":             fmt.Sprintf("00000000-0000-4000-8000-%012d", index),
				"resourceVersion": fmt.Sprintf("%d", 100000+index),
				"creationTimestamp": fmt.Sprintf(
					"2026-0%d-1%dT0%d:00:00Z", index%9+1, index%9, index%9,
				),
			},
			Spec: map[string]any{
				"nodeName":           fmt.Sprintf("node-%03d", index%64),
				"serviceAccountName": "default",
				"containers": []map[string]any{{
					"name":  "application",
					"image": "registry.example.test/application:1.4.2",
					"resources": map[string]any{
						"requests": map[string]string{"cpu": "250m", "memory": "256Mi"},
					},
				}},
			},
			Status: map[string]any{
				"phase":  "Running",
				"podIP":  fmt.Sprintf("10.%d.%d.%d", index/65536%256, index/256%256, index%256),
				"hostIP": fmt.Sprintf("192.168.%d.%d", index/256%256, index%256),
			},
			Labels: map[string]string{
				"app":                          fmt.Sprintf("service-%03d", index%200),
				"app.kubernetes.io/managed-by": "zke",
			},
		})
	}
	body, err := json.Marshal(map[string]any{
		"apiVersion": "v1",
		"kind":       "PodList",
		"items":      list,
	})
	if err != nil {
		panic(err)
	}
	return body
}

// BenchmarkEncodeResourceBody measures what compression costs the Agent and
// what it saves on the link, for bodies the size the container service actually
// returns. The ratio it prints is what the threshold should be chosen against.
func BenchmarkEncodeResourceBody(b *testing.B) {
	for _, items := range []int{50, 200, 1000, 5000} {
		body := kubernetesListBody(items)
		b.Run(fmt.Sprintf("items=%d/bytes=%d", items, len(body)), func(b *testing.B) {
			request := &agentv1.ResourceRequest{
				AcceptedEncodings: AcceptedResourceEncodings(),
			}
			// A threshold of one, so the measurement is of the compression
			// itself rather than of the decision not to compress.
			encoded := EncodeResourceBody(request, body, 1)
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if result := EncodeResourceBody(request, body, 1); len(result.Wire) == 0 {
					b.Fatal("empty body")
				}
			}
			b.StopTimer()
			b.ReportMetric(
				float64(len(body))/float64(len(encoded.Wire)),
				"compression_ratio",
			)
			b.ReportMetric(float64(len(encoded.Wire)), "wire_bytes")
		})
	}
}

// BenchmarkReadResourceBody is the other half: what the Server pays to inflate
// what the Agent sent.
func BenchmarkReadResourceBody(b *testing.B) {
	for _, items := range []int{200, 1000, 5000} {
		body := kubernetesListBody(items)
		encoded := EncodeResourceBody(&agentv1.ResourceRequest{
			AcceptedEncodings: AcceptedResourceEncodings(),
		}, body, 1)
		response := &agentv1.ResourceResponse{
			Result:          agentv1.ResultCode_RESULT_CODE_OK,
			BodySize:        uint64(len(encoded.Wire)),
			ContentEncoding: encoded.Encoding,
			DecodedBodySize: encoded.DecodedSize,
		}
		b.Run(fmt.Sprintf("items=%d/bytes=%d", items, len(body)), func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				var decoded bytes.Buffer
				decoded.Grow(len(body))
				if err := readResourceBody(
					bytes.NewReader(encoded.Wire),
					response,
					&decoded,
				); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
