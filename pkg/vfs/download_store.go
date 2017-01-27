package vfs

import (
	"encoding/hex"
	"time"

	"github.com/cozy/cozy-stack/pkg/crypto"
)

// A DownloadStore is essentially an object to store Archives & Files by keys
type DownloadStore interface {
	AddFile(f string) (string, error)
	AddArchive(a *Archive) (string, error)
	GetFile(k string) (string, error)
	GetArchive(k string) (*Archive, error)
}

type fileRef struct {
	Path      string
	ExpiresAt time.Time
}

// downloadStoreTTL is the time an Archive stay alive
const downloadStoreTTL = 1 * time.Hour

var storeStore map[string]*memStore

// GetStore returns the DownloadStore for the given Instance
func GetStore(domain string) DownloadStore {
	if storeStore == nil {
		storeStore = make(map[string]*memStore)
	}
	store, ok := storeStore[domain]
	if ok {
		return store
	}
	storeStore[domain] = &memStore{
		Archives: make(map[string]*Archive),
		Files:    make(map[string]*fileRef),
	}
	return storeStore[domain]
}

type memStore struct {
	Archives map[string]*Archive
	Files    map[string]*fileRef
}

func cleanDownloadStore() {
	now := time.Now()
	for i, s := range storeStore {
		for k, f := range s.Files {
			if now.After(f.ExpiresAt) {
				delete(s.Files, k)
			}
		}
		for k, a := range s.Archives {
			if now.After(a.ExpiresAt) {
				delete(s.Archives, k)
			}
		}
		if len(s.Files) == 0 && len(s.Archives) == 0 {
			delete(storeStore, i)
		}
	}
}

func (s *memStore) makeSecret() string {
	return hex.EncodeToString(crypto.GenerateRandomBytes(16))
}

func (s *memStore) AddFile(f string) (string, error) {
	fref := &fileRef{
		Path:      f,
		ExpiresAt: time.Now().Add(downloadStoreTTL),
	}
	key := s.makeSecret()
	s.Files[key] = fref
	return key, nil
}

func (s *memStore) AddArchive(a *Archive) (string, error) {
	a.ExpiresAt = time.Now().Add(downloadStoreTTL)
	key := s.makeSecret()
	s.Archives[key] = a
	return key, nil
}

func (s *memStore) GetFile(k string) (string, error) {
	f, ok := s.Files[k]
	if !ok {
		return "", nil
	}
	if time.Now().After(f.ExpiresAt) {
		delete(s.Files, k)
		return "", nil
	}
	return f.Path, nil
}

func (s *memStore) GetArchive(k string) (*Archive, error) {
	a, ok := s.Archives[k]
	if !ok {
		return nil, nil
	}
	if time.Now().After(a.ExpiresAt) {
		delete(s.Files, k)
		return nil, nil
	}
	return a, nil
}
