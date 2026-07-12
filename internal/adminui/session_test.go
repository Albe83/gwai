package adminui

import (
	"sync"
	"testing"
	"time"
)

func TestOpaqueSessionExpiryCSRFAndDestruction(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	store := newSessionStore(func() time.Time { return now }, nil, 2*time.Minute)
	first, err := store.create(false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.create(true)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || first.CSRFToken == second.CSRFToken || len(first.ID) < 40 || len(first.CSRFToken) < 40 {
		t.Fatalf("tokens are not independent opaque values: first=%+v second=%+v", first, second)
	}
	if !store.verifyCSRF(first.ID, first.CSRFToken) || store.verifyCSRF(first.ID, first.CSRFToken+"x") || store.verifyCSRF("tampered", first.CSRFToken) {
		t.Fatal("CSRF verification accepted an invalid pair")
	}
	store.destroy(first.ID)
	if _, ok := store.load(first.ID); ok {
		t.Fatal("destroyed session remained available")
	}
	now = now.Add(3 * time.Minute)
	if _, ok := store.load(second.ID); ok || store.verifyCSRF(second.ID, second.CSRFToken) {
		t.Fatal("expired session remained valid")
	}
}

func TestKeyCreationTokenIsSessionBoundExpiringAndConsumedAtomically(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	store := newSessionStore(func() time.Time { return now }, nil, 10*time.Minute)
	owner, err := store.create(true)
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.create(true)
	if err != nil {
		t.Fatal(err)
	}
	operationToken, err := store.issueKeyCreationToken(owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if store.consumeKeyCreationToken(other.ID, operationToken) {
		t.Fatal("another session consumed the key-creation token")
	}

	var successes int
	var mu sync.Mutex
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if store.consumeKeyCreationToken(owner.ID, operationToken) {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wait.Wait()
	if successes != 1 {
		t.Fatalf("operation token succeeded %d times, want once", successes)
	}

	operationToken, err = store.issueKeyCreationToken(owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(11 * time.Minute)
	if store.consumeKeyCreationToken(owner.ID, operationToken) {
		t.Fatal("expired operation token remained available")
	}
}
