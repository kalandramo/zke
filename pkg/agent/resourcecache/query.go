package resourcecache

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/togettoyou/zke/pkg/shared/resourcesummary"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// DefaultQueryLimit matches the page size the Server's own list APIs use,
	// so a cached list and an uncached one page the same way.
	DefaultQueryLimit = 100
	MaxQueryLimit     = 500
	// A paging session holds the sorted keys one query matched, so its later
	// pages describe the same set of objects even while the Cluster changes
	// underneath. They are bounded in every direction: how many exist, how long
	// each lives, and how many keys one may hold.
	maxPagingSessions    = 64
	maxPagingSessionAge  = 2 * time.Minute
	maxPagingSessionKeys = 200_000
)

// ErrQueryUnsupported is a query the cache cannot answer exactly — a field
// selector it does not index, most often. The caller reads Kubernetes instead
// rather than being given an answer that is nearly right.
var ErrQueryUnsupported = errors.New("resource cache cannot answer this query")

// ErrPageToken is a continuation that does not belong to this cache, this
// query, or this moment.
var ErrPageToken = errors.New("resource cache page token is not valid")

// Query is one page request against the cache.
type Query struct {
	Resource      schema.GroupVersionResource
	Namespace     string
	LabelSelector string
	FieldSelector string
	Limit         int
	PageToken     string
}

// pagingSession is the ordered set of keys one query matched, kept so that its
// later pages read the same set rather than re-matching a Cluster that has
// moved on. The summaries themselves are read fresh on every page, so a page
// never shows an object as it was rather than as it is.
type pagingSession struct {
	fingerprint string
	keys        []objectKey
	snapshot    string
	createdAt   time.Time
}

type pagingSessions struct {
	mutex    sync.Mutex
	sessions map[string]*pagingSession
	now      func() time.Time
}

func newPagingSessions(now func() time.Time) *pagingSessions {
	if now == nil {
		now = time.Now
	}
	return &pagingSessions{
		sessions: make(map[string]*pagingSession),
		now:      now,
	}
}

// Summaries answers one page from the cache.
//
// A resource this cache does not follow, a query it cannot express, or a
// section that has not listed yet all come back as errors the caller can tell
// apart, because each of them means something different: read Kubernetes
// instead, read Kubernetes instead, and wait.
func (instance *Cache) Summaries(query Query) (resourcesummary.Page, error) {
	if instance == nil {
		return resourcesummary.Page{}, ErrResourceNotCached
	}
	target, exists := instance.stores[query.Resource]
	if !exists || instance.unavailable(query.Resource) {
		return resourcesummary.Page{}, ErrResourceNotCached
	}
	if query.Namespace != "" && !target.kind.namespaced {
		return resourcesummary.Page{}, ErrQueryUnsupported
	}
	limit := query.Limit
	if limit <= 0 {
		limit = DefaultQueryLimit
	}
	if limit > MaxQueryLimit {
		limit = MaxQueryLimit
	}
	selector, err := labels.Parse(query.LabelSelector)
	if err != nil {
		return resourcesummary.Page{}, ErrQueryUnsupported
	}
	matcher, err := parseFieldSelector(query.Resource, query.FieldSelector)
	if err != nil {
		return resourcesummary.Page{}, err
	}

	if query.PageToken != "" {
		return instance.continuePage(target, query, limit)
	}
	return instance.firstPage(target, query, selector, matcher, limit)
}

func (instance *Cache) firstPage(
	target *store,
	query Query,
	selector labels.Selector,
	matcher fieldMatcher,
	limit int,
) (resourcesummary.Page, error) {
	keys, snapshot, resourceVersion, synced := target.matchingKeys(
		query.Namespace,
		selector,
		matcher,
	)
	if !synced {
		return resourcesummary.Page{}, ErrNotSynced
	}
	page, remaining := cut(target, keys, 0, limit)
	page.Snapshot = snapshot
	page.ResourceVersion = resourceVersion
	page.Remaining = remaining
	if remaining > 0 {
		token, err := instance.paging.open(
			queryFingerprint(query),
			keys,
			snapshot,
			limit,
		)
		if err != nil {
			return resourcesummary.Page{}, err
		}
		page.NextPageToken = token
	}
	return page, nil
}

func (instance *Cache) continuePage(
	target *store,
	query Query,
	limit int,
) (resourcesummary.Page, error) {
	session, offset, err := instance.paging.resume(
		query.PageToken,
		queryFingerprint(query),
	)
	if err != nil {
		return resourcesummary.Page{}, err
	}
	resourceVersion, synced := target.state()
	if !synced {
		return resourcesummary.Page{}, ErrNotSynced
	}
	page, remaining := cut(target, session.keys, offset, limit)
	page.Snapshot = session.snapshot
	page.ResourceVersion = resourceVersion
	page.Remaining = remaining
	if remaining > 0 {
		token, err := instance.paging.advance(query.PageToken, offset+limit)
		if err != nil {
			return resourcesummary.Page{}, err
		}
		page.NextPageToken = token
	}
	return page, nil
}

// cut reads the summaries for one window of keys.
//
// The summaries are read now rather than remembered with the keys: a page shows
// each object as it currently is, and an object deleted since the query started
// simply is not there any more.
func cut(
	target *store,
	keys []objectKey,
	offset int,
	limit int,
) (resourcesummary.Page, int64) {
	page := resourcesummary.Page{Items: make([]json.RawMessage, 0, limit)}
	if offset < 0 || offset >= len(keys) {
		return page, 0
	}
	end := min(offset+limit, len(keys))
	for _, key := range keys[offset:end] {
		summary, found := target.summary(key)
		if !found {
			continue
		}
		encoded, err := json.Marshal(summary)
		if err != nil {
			continue
		}
		page.Items = append(page.Items, encoded)
	}
	return page, int64(len(keys) - end)
}

// matchingKeys walks the section once and returns the keys that match, sorted
// by Namespace then Name — the order Kubernetes itself lists in, so a cached
// page and an uncached one put the same object in the same place.
func (target *store) matchingKeys(
	namespace string,
	selector labels.Selector,
	matcher fieldMatcher,
) ([]objectKey, string, string, bool) {
	target.mutex.RLock()
	defer target.mutex.RUnlock()
	if !target.synced {
		return nil, "", "", false
	}
	keys := make([]objectKey, 0, len(target.items))
	for key, item := range target.items {
		if namespace != "" && key.namespace != namespace {
			continue
		}
		if !selector.Empty() && !selector.Matches(labels.Set(item.labels)) {
			continue
		}
		if matcher != nil && !matcher(item) {
			continue
		}
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool {
		if keys[left].namespace != keys[right].namespace {
			return keys[left].namespace < keys[right].namespace
		}
		return keys[left].name < keys[right].name
	})
	return keys,
		strconv.FormatUint(target.generation, 10),
		target.resourceVersion,
		true
}

// state is the cheap read a continuation needs: it does not re-match the whole
// section just to learn the ResourceVersion.
func (target *store) state() (string, bool) {
	target.mutex.RLock()
	defer target.mutex.RUnlock()
	return target.resourceVersion, target.synced
}

func (target *store) summary(key objectKey) (any, bool) {
	target.mutex.RLock()
	defer target.mutex.RUnlock()
	item, found := target.items[key]
	if !found {
		return nil, false
	}
	return item.summary, true
}

// queryFingerprint is everything a continuation must still match. A token
// issued for one query cannot be used to walk another, and one issued by
// another Agent's cache is not in this Agent's session table at all.
func queryFingerprint(query Query) string {
	return strings.Join([]string{
		query.Resource.Group,
		query.Resource.Version,
		query.Resource.Resource,
		query.Namespace,
		query.LabelSelector,
		query.FieldSelector,
	}, "\x00")
}

func (sessions *pagingSessions) open(
	fingerprint string,
	keys []objectKey,
	snapshot string,
	offset int,
) (string, error) {
	if len(keys) > maxPagingSessionKeys {
		// A query this wide is not something a page ever walks to the end of,
		// and remembering its keys would let one request hold a large slice for
		// the lifetime of the session.
		return "", ErrQueryUnsupported
	}
	identifier, err := newSessionIdentifier()
	if err != nil {
		return "", err
	}
	now := sessions.now()
	sessions.mutex.Lock()
	defer sessions.mutex.Unlock()
	sessions.expireLocked(now)
	if len(sessions.sessions) >= maxPagingSessions {
		sessions.evictOldestLocked()
	}
	sessions.sessions[identifier] = &pagingSession{
		fingerprint: fingerprint,
		keys:        keys,
		snapshot:    snapshot,
		createdAt:   now,
	}
	return encodePageToken(identifier, offset), nil
}

func (sessions *pagingSessions) resume(
	token string,
	fingerprint string,
) (*pagingSession, int, error) {
	identifier, offset, err := decodePageToken(token)
	if err != nil {
		return nil, 0, err
	}
	now := sessions.now()
	sessions.mutex.Lock()
	defer sessions.mutex.Unlock()
	sessions.expireLocked(now)
	session, found := sessions.sessions[identifier]
	if !found {
		return nil, 0, fmt.Errorf("%w: unknown or expired page", ErrPageToken)
	}
	if session.fingerprint != fingerprint {
		return nil, 0, fmt.Errorf("%w: query does not match the page", ErrPageToken)
	}
	if offset < 0 || offset > len(session.keys) {
		return nil, 0, fmt.Errorf("%w: position is outside the page", ErrPageToken)
	}
	return session, offset, nil
}

func (sessions *pagingSessions) advance(token string, offset int) (string, error) {
	identifier, _, err := decodePageToken(token)
	if err != nil {
		return "", err
	}
	return encodePageToken(identifier, offset), nil
}

func (sessions *pagingSessions) expireLocked(now time.Time) {
	for identifier, session := range sessions.sessions {
		if now.Sub(session.createdAt) > maxPagingSessionAge {
			delete(sessions.sessions, identifier)
		}
	}
}

func (sessions *pagingSessions) evictOldestLocked() {
	oldest := ""
	var oldestAt time.Time
	for identifier, session := range sessions.sessions {
		if oldest == "" || session.createdAt.Before(oldestAt) {
			oldest = identifier
			oldestAt = session.createdAt
		}
	}
	if oldest != "" {
		delete(sessions.sessions, oldest)
	}
}

func newSessionIdentifier() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", errors.New("generate resource cache page identifier")
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

// The token is opaque to its holder: it carries a session identifier nobody can
// guess and a position, and it means nothing outside the Agent process that
// issued it.
func encodePageToken(identifier string, offset int) string {
	return base64.RawURLEncoding.EncodeToString(
		[]byte(identifier + ":" + strconv.Itoa(offset)),
	)
}

func decodePageToken(token string) (string, int, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", 0, fmt.Errorf("%w: malformed token", ErrPageToken)
	}
	identifier, position, found := strings.Cut(string(decoded), ":")
	if !found || identifier == "" {
		return "", 0, fmt.Errorf("%w: malformed token", ErrPageToken)
	}
	offset, err := strconv.Atoi(position)
	if err != nil {
		return "", 0, fmt.Errorf("%w: malformed position", ErrPageToken)
	}
	return identifier, offset, nil
}
