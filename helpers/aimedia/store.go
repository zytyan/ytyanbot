package aimedia

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const hashLength = sha256.Size * 2

type Object struct {
	SHA256       string
	RelativePath string
	Size         int64
}

type Store struct {
	root string
}

func NewStore(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("AI media root is empty")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve AI media root: %w", err)
	}
	store := &Store{root: absRoot}
	if err = store.ensureDirectory(absRoot); err != nil {
		return nil, err
	}
	if err = store.ensureDirectory(filepath.Join(absRoot, "sha256")); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Root() string {
	return s.root
}

func relativeObjectPath(hash string) (string, error) {
	if len(hash) != hashLength || strings.ToLower(hash) != hash {
		return "", fmt.Errorf("invalid SHA-256 hash %q", hash)
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return "", fmt.Errorf("invalid SHA-256 hash %q: %w", hash, err)
	}
	return path.Join("sha256", hash[:2], hash), nil
}

func (s *Store) absolutePath(hash string) (string, string, error) {
	relative, err := relativeObjectPath(hash)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(s.root, filepath.FromSlash(relative)), relative, nil
}

func (s *Store) ensureDirectory(directory string) error {
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return fmt.Errorf("create AI media directory %s: %w", directory, err)
	}
	if err := os.Chmod(directory, 0o750); err != nil {
		return fmt.Errorf("set AI media directory permissions %s: %w", directory, err)
	}
	return nil
}

func (s *Store) Put(data []byte) (Object, error) {
	hashBytes := sha256.Sum256(data)
	hash := hex.EncodeToString(hashBytes[:])
	absolute, relative, err := s.absolutePath(hash)
	if err != nil {
		return Object{}, err
	}
	object := Object{SHA256: hash, RelativePath: relative, Size: int64(len(data))}
	if err = s.Verify(hash, object.Size); err == nil {
		return object, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		// A corrupt object is repaired from the caller's verified bytes below.
	}

	directory := filepath.Dir(absolute)
	if err = s.ensureDirectory(directory); err != nil {
		return Object{}, err
	}
	temporary, err := os.CreateTemp(directory, ".tmp-")
	if err != nil {
		return Object{}, fmt.Errorf("create temporary AI media object: %w", err)
	}
	temporaryName := temporary.Name()
	keepTemporary := true
	defer func() {
		if keepTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	if err = temporary.Chmod(0o640); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return Object{}, fmt.Errorf("write temporary AI media object: %w", err)
	}
	if err = os.Rename(temporaryName, absolute); err != nil {
		return Object{}, fmt.Errorf("publish AI media object: %w", err)
	}
	keepTemporary = false
	if err = syncDirectory(directory); err != nil {
		return Object{}, err
	}
	if err = s.Verify(hash, object.Size); err != nil {
		return Object{}, err
	}
	return object, nil
}

func (s *Store) Open(hash string) (*os.File, error) {
	absolute, _, err := s.absolutePath(hash)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(absolute)
	if err != nil {
		return nil, fmt.Errorf("open AI media object %s: %w", hash, err)
	}
	return file, nil
}

func (s *Store) Verify(hash string, expectedSize int64) error {
	file, err := s.Open(hash)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat AI media object %s: %w", hash, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("AI media object %s is not a regular file", hash)
	}
	if expectedSize >= 0 && info.Size() != expectedSize {
		return fmt.Errorf("AI media object %s size mismatch: got %d want %d", hash, info.Size(), expectedSize)
	}
	digest := sha256.New()
	if _, err = io.Copy(digest, file); err != nil {
		return fmt.Errorf("hash AI media object %s: %w", hash, err)
	}
	if actual := hex.EncodeToString(digest.Sum(nil)); actual != hash {
		return fmt.Errorf("AI media object %s hash mismatch: got %s", hash, actual)
	}
	return nil
}

func (s *Store) CollectGarbage(referenced map[string]struct{}, grace time.Duration, dryRun bool) ([]Object, error) {
	cutoff := time.Now().Add(-grace)
	objectsRoot := filepath.Join(s.root, "sha256")
	var collected []Object
	err := filepath.WalkDir(objectsRoot, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		hash := entry.Name()
		relative, hashErr := relativeObjectPath(hash)
		if hashErr != nil || filepath.Clean(filePath) != filepath.Join(s.root, filepath.FromSlash(relative)) {
			return nil
		}
		if _, keep := referenced[hash]; keep {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(cutoff) {
			return nil
		}
		object := Object{SHA256: hash, RelativePath: relative, Size: info.Size()}
		collected = append(collected, object)
		if dryRun {
			return nil
		}
		if err = os.Remove(filePath); err != nil {
			return fmt.Errorf("remove unreferenced AI media object %s: %w", hash, err)
		}
		return syncDirectory(filepath.Dir(filePath))
	})
	if err != nil {
		return nil, fmt.Errorf("collect AI media garbage: %w", err)
	}
	return collected, nil
}

func syncDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open AI media directory for sync: %w", err)
	}
	defer file.Close()
	if err = file.Sync(); err != nil {
		return fmt.Errorf("sync AI media directory: %w", err)
	}
	return nil
}
