package aimedia

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStorePutDeduplicatesAndVerifies(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "media"))
	require.NoError(t, err)
	data := []byte("same media payload")
	first, err := store.Put(data)
	require.NoError(t, err)
	second, err := store.Put(data)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, int64(len(data)), first.Size)
	require.NoError(t, store.Verify(first.SHA256, first.Size))

	file, err := store.Open(first.SHA256)
	require.NoError(t, err)
	saved := make([]byte, len(data))
	_, err = file.Read(saved)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	require.True(t, bytes.Equal(data, saved))

	info, err := os.Stat(filepath.Join(store.Root(), filepath.FromSlash(first.RelativePath)))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o640), info.Mode().Perm())
	for _, directory := range []string{store.Root(), filepath.Join(store.Root(), "sha256")} {
		info, err = os.Stat(directory)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o750), info.Mode().Perm())
	}
}

func TestStoreConcurrentPut(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "media"))
	require.NoError(t, err)
	data := []byte("concurrent payload")
	const writers = 20
	objects := make(chan Object, writers)
	errors := make(chan error, writers)
	var wait sync.WaitGroup
	for range writers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			object, putErr := store.Put(data)
			objects <- object
			errors <- putErr
		}()
	}
	wait.Wait()
	close(objects)
	close(errors)
	for putErr := range errors {
		require.NoError(t, putErr)
	}
	var expected Object
	for object := range objects {
		if expected.SHA256 == "" {
			expected = object
		}
		require.Equal(t, expected, object)
	}
	require.NoError(t, store.Verify(expected.SHA256, int64(len(data))))
}

func TestStoreRepairsCorruptObject(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "media"))
	require.NoError(t, err)
	data := []byte("correct payload")
	object, err := store.Put(data)
	require.NoError(t, err)
	absolute := filepath.Join(store.Root(), filepath.FromSlash(object.RelativePath))
	require.NoError(t, os.WriteFile(absolute, []byte("broken"), 0o640))
	require.ErrorContains(t, store.Verify(object.SHA256, object.Size), "size mismatch")

	repaired, err := store.Put(data)
	require.NoError(t, err)
	require.Equal(t, object, repaired)
	require.NoError(t, store.Verify(object.SHA256, object.Size))
}

func TestStoreGarbageCollectionOnlyDeletesUnreferencedObjects(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "media"))
	require.NoError(t, err)
	keep, err := store.Put([]byte("keep"))
	require.NoError(t, err)
	remove, err := store.Put([]byte("remove"))
	require.NoError(t, err)
	old := time.Now().Add(-2 * time.Hour)
	for _, object := range []Object{keep, remove} {
		absolute := filepath.Join(store.Root(), filepath.FromSlash(object.RelativePath))
		require.NoError(t, os.Chtimes(absolute, old, old))
	}

	referenced := map[string]struct{}{keep.SHA256: {}}
	dryRun, err := store.CollectGarbage(referenced, time.Hour, true)
	require.NoError(t, err)
	require.Equal(t, []Object{remove}, dryRun)
	require.NoError(t, store.Verify(remove.SHA256, remove.Size))

	deleted, err := store.CollectGarbage(referenced, time.Hour, false)
	require.NoError(t, err)
	require.Equal(t, []Object{remove}, deleted)
	require.NoError(t, store.Verify(keep.SHA256, keep.Size))
	_, err = store.Open(remove.SHA256)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestStoreRejectsInvalidHashes(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "media"))
	require.NoError(t, err)
	_, err = store.Open("../escape")
	require.ErrorContains(t, err, "invalid SHA-256")
}
