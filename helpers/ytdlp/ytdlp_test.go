package ytdlp

import (
	"errors"
	"testing"
	"time"
)

func TestRunBBDownConcurrentWaitsAndPreservesBothErrors(t *testing.T) {
	downloadErr := errors.New("download failed")
	metadataErr := errors.New("metadata failed")
	metadataStarted := make(chan struct{})
	metadataContinue := make(chan struct{})
	downloadDone := make(chan struct{})
	returned := make(chan struct{})
	var gotInfoErr error
	var gotDownloadErr error
	go func() {
		_, gotInfoErr, gotDownloadErr = runBBDownConcurrent(
			func() error {
				close(downloadDone)
				return downloadErr
			},
			func() (Info, error) {
				close(metadataStarted)
				<-metadataContinue
				return Info{}, metadataErr
			},
		)
		close(returned)
	}()
	<-metadataStarted
	<-downloadDone
	select {
	case <-returned:
		t.Fatal("BBDown orchestration returned before metadata completed")
	case <-time.After(50 * time.Millisecond):
	}
	close(metadataContinue)
	<-returned
	if !errors.Is(gotInfoErr, metadataErr) {
		t.Fatalf("metadata error = %v, want %v", gotInfoErr, metadataErr)
	}
	if !errors.Is(gotDownloadErr, downloadErr) {
		t.Fatalf("download error = %v, want %v", gotDownloadErr, downloadErr)
	}
}

func TestRunBBDownConcurrentConvertsMetadataPanic(t *testing.T) {
	_, infoErr, downloadErr := runBBDownConcurrent(
		func() error { return nil },
		func() (Info, error) { panic("metadata panic") },
	)
	if downloadErr != nil {
		t.Fatalf("download error = %v, want nil", downloadErr)
	}
	if infoErr == nil {
		t.Fatal("metadata panic was not converted to an error")
	}
}
