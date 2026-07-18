package handlers

import (
	"errors"
	"sync/atomic"
	"testing"
)

func TestDeliverDownloadedResultResendsForEveryWaiter(t *testing.T) {
	result := &DlResult{}
	uploadStarted := make(chan struct{})
	uploadContinue := make(chan struct{})
	secondStarted := make(chan struct{})
	errs := make(chan error, 2)
	var uploads atomic.Int32
	var resends atomic.Int32
	upload := func() error {
		uploads.Add(1)
		result.FileID = "telegram-file-id"
		close(uploadStarted)
		<-uploadContinue
		return nil
	}
	resend := func() error {
		resends.Add(1)
		if result.FileID == "" {
			t.Error("resend ran before the upload published its file ID")
		}
		return nil
	}

	go func() { errs <- deliverDownloadedResult(result, upload, resend) }()
	<-uploadStarted
	go func() {
		close(secondStarted)
		errs <- deliverDownloadedResult(result, upload, resend)
	}()
	<-secondStarted
	close(uploadContinue)

	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("delivery error: %v", err)
		}
	}
	if got := uploads.Load(); got != 1 {
		t.Fatalf("uploads = %d, want 1", got)
	}
	if got := resends.Load(); got != 1 {
		t.Fatalf("resends = %d, want 1", got)
	}
}

func TestDeliverDownloadedResultSharesUploadFailure(t *testing.T) {
	result := &DlResult{}
	wantErr := errors.New("upload failed")
	var resends atomic.Int32
	for range 2 {
		err := deliverDownloadedResult(result, func() error { return wantErr }, func() error {
			resends.Add(1)
			return nil
		})
		if !errors.Is(err, wantErr) {
			t.Fatalf("delivery error = %v, want %v", err, wantErr)
		}
	}
	if got := resends.Load(); got != 0 {
		t.Fatalf("resends after failed upload = %d, want 0", got)
	}
}
