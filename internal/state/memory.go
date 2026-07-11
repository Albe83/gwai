package state

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
)

type memoryValue struct {
	value json.RawMessage
	etag  uint64
}

type MemoryStore struct {
	mu     sync.RWMutex
	values map[string]memoryValue
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{values: make(map[string]memoryValue)}
}

func (s *MemoryStore) Get(_ context.Context, key string) (Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.values[key]
	if !ok {
		return Entry{}, nil
	}
	return Entry{
		Value:  append(json.RawMessage(nil), value.value...),
		ETag:   strconv.FormatUint(value.etag, 10),
		Exists: true,
	}, nil
}

func (s *MemoryStore) Transact(_ context.Context, operations []Operation) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, operation := range operations {
		current, exists := s.values[operation.Key]
		if operation.ETag == "" {
			continue
		}
		if !exists || strconv.FormatUint(current.etag, 10) != operation.ETag {
			return fmt.Errorf("%w: key %q", ErrConflict, operation.Key)
		}
	}

	for _, operation := range operations {
		switch operation.Type {
		case Upsert:
			current := s.values[operation.Key]
			current.etag++
			if current.etag == 0 {
				current.etag = 1
			}
			current.value = append(json.RawMessage(nil), operation.Value...)
			s.values[operation.Key] = current
		case Delete:
			delete(s.values, operation.Key)
		default:
			return fmt.Errorf("unsupported memory state operation %q", operation.Type)
		}
	}
	return nil
}
