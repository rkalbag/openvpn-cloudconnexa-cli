package keychain

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

func testStore(t *testing.T, s Store) {
	t.Helper()
	if _, err := s.Get("acme", "c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get before set: %v", err)
	}
	if err := s.Set("acme", "c1", "secret-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("acme", "c1", "secret-2"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("acme", "c2", "other"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get("acme", "c1"); err != nil || got != "secret-2" {
		t.Fatalf("get = %q, %v", got, err)
	}
	if err := s.Delete("acme", "c1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("acme", "c1"); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if _, err := s.Get("acme", "c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
	if got, _ := s.Get("acme", "c2"); got != "other" {
		t.Fatalf("other account affected: %q", got)
	}
}

func TestMemory(t *testing.T) {
	testStore(t, NewMemory())
}

// TestOSWithMockProvider exercises the OS wrapper against go-keyring's
// in-memory provider, so no real keychain is touched.
func TestOSWithMockProvider(t *testing.T) {
	keyring.MockInit()
	testStore(t, OS{})
}

func TestAccount(t *testing.T) {
	if got := Account("acme", "c1"); got != "acme/c1" {
		t.Fatal(got)
	}
}
