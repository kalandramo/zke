package resourcesummary

import "encoding/json"

// Page is one page of summaries as it travels from Agent to Server.
//
// Items stay raw here because one page holds one resource's summaries and the
// caller already knows which: it asked for Pods, or Nodes, or Deployments. That
// keeps the envelope one type rather than one per resource, without giving up
// the typed model on either end.
type Page struct {
	Items []json.RawMessage `json:"items"`
	// NextPageToken is opaque and bound to the query and the cache snapshot it
	// was issued against. It is empty on the last page.
	NextPageToken string `json:"next_page_token,omitempty"`
	// ResourceVersion of the cached resource at the time this page was cut. It
	// is that one resource's Kubernetes token, and says nothing about any other.
	ResourceVersion string `json:"resource_version"`
	// Snapshot identifies the cache contents this page was cut from. Two pages
	// carrying the same value describe the same set of objects.
	Snapshot string `json:"snapshot"`
	// Remaining is how many matching objects follow this page.
	Remaining int64 `json:"remaining"`
}
