package visit

import (
	"crypto/rand"
	"errors"
	"sort"
	"sync"
	"time"
)

const (
	VisitIDSize = 16
	VisitTTL    = 90 * time.Second
)

var (
	ErrVisitNotFound = errors.New("visit not found")
	ErrVisitExpired  = errors.New("visit expired")
)

type ownerKey struct {
	ownerID   uint64
	visitorID uint64
}

type ownerVisit struct {
	visitID     [VisitIDSize]byte
	requestID   string
	expiresAtMS int64
}

type visitorVisit struct {
	ownerID uint64
	visitID [VisitIDSize]byte
}

// VisitRecord is a copy-safe snapshot of one active owner-side visit lease.
type VisitRecord struct {
	VisitorPlayerID uint64
	VisitID         []byte
	ExpiresAtMS     int64
}

type Registry struct {
	mu       sync.Mutex
	now      func() time.Time
	owners   map[ownerKey]ownerVisit
	visitors map[uint64]visitorVisit
}

func NewRegistry(now func() time.Time) *Registry {
	if now == nil {
		now = time.Now
	}
	return &Registry{
		now:      now,
		owners:   make(map[ownerKey]ownerVisit),
		visitors: make(map[uint64]visitorVisit),
	}
}

func (r *Registry) EnterOwner(
	ownerID, visitorID uint64,
	requestID string,
) ([]byte, int64, error) {
	if ownerID == 0 || visitorID == 0 || ownerID == visitorID || requestID == "" {
		return nil, 0, errors.New("invalid visit enter")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := ownerKey{ownerID: ownerID, visitorID: visitorID}
	nowMS := r.now().UnixMilli()
	if existing, ok := r.owners[key]; ok &&
		existing.requestID == requestID &&
		existing.expiresAtMS > nowMS {
		existing.expiresAtMS = r.now().Add(VisitTTL).UnixMilli()
		r.owners[key] = existing
		return append([]byte(nil), existing.visitID[:]...), existing.expiresAtMS, nil
	}
	var id [VisitIDSize]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, 0, err
	}
	record := ownerVisit{
		visitID: id, requestID: requestID,
		expiresAtMS: r.now().Add(VisitTTL).UnixMilli(),
	}
	r.owners[key] = record
	return append([]byte(nil), id[:]...), record.expiresAtMS, nil
}

func (r *Registry) SetVisitor(visitorID, ownerID uint64, visitID []byte) error {
	id, err := parseVisitID(visitID)
	if err != nil || visitorID == 0 || ownerID == 0 || visitorID == ownerID {
		return errors.New("invalid visitor visit")
	}
	r.mu.Lock()
	r.visitors[visitorID] = visitorVisit{ownerID: ownerID, visitID: id}
	r.mu.Unlock()
	return nil
}

func (r *Registry) ValidateVisitor(visitorID, ownerID uint64, visitID []byte) error {
	id, err := parseVisitID(visitID)
	if err != nil {
		return ErrVisitNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.visitors[visitorID]
	if !ok || record.ownerID != ownerID || record.visitID != id {
		return ErrVisitNotFound
	}
	return nil
}

func (r *Registry) ValidateOwner(ownerID, visitorID uint64, visitID []byte) error {
	id, err := parseVisitID(visitID)
	if err != nil {
		return ErrVisitNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := ownerKey{ownerID: ownerID, visitorID: visitorID}
	record, ok := r.owners[key]
	if !ok || record.visitID != id {
		return ErrVisitNotFound
	}
	if record.expiresAtMS <= r.now().UnixMilli() {
		delete(r.owners, key)
		return ErrVisitExpired
	}
	return nil
}

func (r *Registry) RefreshOwner(
	ownerID, visitorID uint64,
	visitID []byte,
) (int64, error) {
	if err := r.ValidateOwner(ownerID, visitorID, visitID); err != nil {
		return 0, err
	}
	id, _ := parseVisitID(visitID)
	r.mu.Lock()
	defer r.mu.Unlock()
	key := ownerKey{ownerID: ownerID, visitorID: visitorID}
	record, ok := r.owners[key]
	if !ok || record.visitID != id {
		return 0, ErrVisitNotFound
	}
	record.expiresAtMS = r.now().Add(VisitTTL).UnixMilli()
	r.owners[key] = record
	return record.expiresAtMS, nil
}

func (r *Registry) ExitOwner(ownerID, visitorID uint64, visitID []byte) error {
	if err := r.ValidateOwner(ownerID, visitorID, visitID); err != nil {
		return err
	}
	r.mu.Lock()
	delete(r.owners, ownerKey{ownerID: ownerID, visitorID: visitorID})
	r.mu.Unlock()
	return nil
}

// CancelOwner removes only the visit identified by visitID. It is used to
// roll back an enter whose snapshot could not be built without deleting a
// newer replacement created concurrently for the same owner and visitor.
func (r *Registry) CancelOwner(ownerID, visitorID uint64, visitID []byte) bool {
	id, err := parseVisitID(visitID)
	if err != nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := ownerKey{ownerID: ownerID, visitorID: visitorID}
	record, ok := r.owners[key]
	if !ok || record.visitID != id {
		return false
	}
	delete(r.owners, key)
	return true
}

func (r *Registry) ClearVisitor(visitorID, ownerID uint64, visitID []byte) error {
	if err := r.ValidateVisitor(visitorID, ownerID, visitID); err != nil {
		return err
	}
	r.mu.Lock()
	delete(r.visitors, visitorID)
	r.mu.Unlock()
	return nil
}

func (r *Registry) CurrentVisitor(visitorID uint64) (uint64, []byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.visitors[visitorID]
	if !ok {
		return 0, nil, false
	}
	return record.ownerID, append([]byte(nil), record.visitID[:]...), true
}

// ListVisitors returns current visitors for ownerID and lazily prunes every
// expired owner-side lease it encounters. Returned visit IDs never alias
// registry storage.
func (r *Registry) ListVisitors(ownerID uint64) []VisitRecord {
	if ownerID == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	nowMS := r.now().UnixMilli()
	records := make([]VisitRecord, 0)
	for key, record := range r.owners {
		if record.expiresAtMS <= nowMS {
			delete(r.owners, key)
			continue
		}
		if key.ownerID != ownerID {
			continue
		}
		records = append(records, VisitRecord{
			VisitorPlayerID: key.visitorID,
			VisitID:         append([]byte(nil), record.visitID[:]...),
			ExpiresAtMS:     record.expiresAtMS,
		})
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].VisitorPlayerID < records[j].VisitorPlayerID
	})
	return records
}

func parseVisitID(raw []byte) ([VisitIDSize]byte, error) {
	var id [VisitIDSize]byte
	if len(raw) != VisitIDSize {
		return id, ErrVisitNotFound
	}
	copy(id[:], raw)
	return id, nil
}
