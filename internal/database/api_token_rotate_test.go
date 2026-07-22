package database

import "testing"

// RotateAPIToken must invalidate the old secret and issue a working new one.
func TestRotateAPIToken(t *testing.T) {
	db, err := NewTestDatabase()
	if err != nil {
		t.Fatalf("NewTestDatabase failed: %v", err)
	}
	oldPlain := "fs_oldsecrettokenvalue1234567890abcdef"
	id, err := db.CreateAPIToken(oldPlain, "test-token", "", 60, 1000)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	// Old token authenticates before rotation.
	if _, err := db.GetAPITokenByToken(oldPlain); err != nil {
		t.Fatalf("GetAPITokenByToken(old) before rotate: %v", err)
	}

	newPlain, err := db.RotateAPIToken(id)
	if err != nil {
		t.Fatalf("RotateAPIToken: %v", err)
	}
	if newPlain == oldPlain {
		t.Fatalf("rotated token equals old token")
	}

	// Old token must NO LONGER authenticate.
	oldGot, err := db.GetAPITokenByToken(oldPlain)
	if err != nil {
		t.Fatalf("GetAPITokenByToken(old) after rotate returned error: %v", err)
	}
	if oldGot != nil {
		t.Fatalf("old token still authenticates after rotation")
	}

	// New token must authenticate.
	if _, err := db.GetAPITokenByToken(newPlain); err != nil {
		t.Fatalf("GetAPITokenByToken(new) after rotate: %v", err)
	}
}
