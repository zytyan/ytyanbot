package ytdlp

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRunErrorRedactsCookieArgument(t *testing.T) {
	err := (&RunError{
		Err:  errors.New("command failed"),
		Args: []string{"video-url", "-c", "SESSDATA=secret; bili_jct=also-secret", "-q", "720P"},
	}).Error()
	if strings.Contains(err, "secret") || strings.Contains(err, "also-secret") {
		t.Fatalf("RunError exposed cookie: %s", err)
	}
	if !strings.Contains(err, `"-c" "[REDACTED]"`) {
		t.Fatalf("RunError did not show redacted cookie argument: %s", err)
	}
}

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

func TestBBDownArgs(t *testing.T) {
	got := bbdownArgs("/run/bili_cookies.json", "/tmp/downloads", true, "BV1example")
	want := []string{
		"--cookie-file", "/run/bili_cookies.json",
		"-work-dir", "/tmp/downloads",
		"-encoding-priority", "HEVC",
		"--audio-only",
		"BV1example",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bbdown-go args = %#v, want %#v", got, want)
	}
}

func TestFindBiliCookieFileReturnsAbsolutePath(t *testing.T) {
	cookieFile := filepath.Join(t.TempDir(), "cookies.json")
	if err := os.WriteFile(cookieFile, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativeCookieFile, err := filepath.Rel(workingDir, cookieFile)
	if err != nil {
		t.Fatal(err)
	}
	oldFiles := biliCookieFiles
	biliCookieFiles = []string{relativeCookieFile}
	t.Cleanup(func() { biliCookieFiles = oldFiles })

	got, err := findBiliCookieFile()
	if err != nil {
		t.Fatalf("find cookie file: %v", err)
	}
	if got != cookieFile {
		t.Fatalf("cookie path = %q, want %q", got, cookieFile)
	}
}

func TestParseBBDownOutput(t *testing.T) {
	workDir := t.TempDir()
	path, err := parseBBDownOutput([]byte(`{"video_path":"BV1example.mp4"}`), workDir, false)
	if err != nil {
		t.Fatalf("parse video output: %v", err)
	}
	if want := filepath.Join(workDir, "BV1example.mp4"); path != want {
		t.Fatalf("video path = %q, want %q", path, want)
	}

	path, err = parseBBDownOutput([]byte(`{"audio_path":"/media/BV1example.m4a"}`), workDir, true)
	if err != nil {
		t.Fatalf("parse audio output: %v", err)
	}
	if path != "/media/BV1example.m4a" {
		t.Fatalf("audio path = %q", path)
	}
}

func TestParseBBDownOutputRequiresExpectedPath(t *testing.T) {
	_, err := parseBBDownOutput([]byte(`{"audio_path":"BV1example.m4a"}`), t.TempDir(), false)
	if err == nil || !strings.Contains(err.Error(), "video_path") {
		t.Fatalf("error = %v, want missing video_path error", err)
	}
}
