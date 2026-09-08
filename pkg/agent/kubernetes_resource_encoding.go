package agent

import (
	"bytes"
	"io"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
)

// resourceResponseBody attaches one JSON body to a Resource response in the
// form the caller asked for.
//
// Every OK body an Agent returns goes through here — a list, a single object,
// a discovery catalog, the object a write produced — so compression is a
// property of the transport rather than of the one endpoint that needed it
// most. A caller that named no encoding, which is every Server predating the
// field, gets the same uncompressed JSON it always did.
func resourceResponseBody(
	request *agentv1.ResourceRequest,
	response *agentv1.ResourceResponse,
	body []byte,
	threshold uint64,
) (*agentv1.ResourceResponse, io.Reader) {
	if response == nil || len(body) == 0 {
		return response, nil
	}
	encoded := agentprotocol.EncodeResourceBody(request, body, threshold)
	response.BodySize = uint64(len(encoded.Wire))
	response.ContentEncoding = encoded.Encoding
	response.DecodedBodySize = encoded.DecodedSize
	return response, bytes.NewReader(encoded.Wire)
}
