package azure

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestNewClientHasBoundedRequestTimeout(t *testing.T) {
	client := NewClient("https://example.test", "key", OcrPath)
	if client.client.Timeout != defaultRequestTimeout {
		t.Fatalf("HTTP timeout = %s, want %s", client.client.Timeout, defaultRequestTimeout)
	}
}

func TestOcrDataContextPropagatesCancellation(t *testing.T) {
	client := NewClient("https://example.test", "key", OcrPath)
	requestStarted := make(chan struct{})
	client.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(requestStarted)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	ocr := &Ocr{Client: *client, ApiVer: "2023-10-01", Features: "Read"}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := ocr.OcrDataContext(ctx, []byte("image"))
		result <- err
	}()
	<-requestStarted
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("OCR error = %v, want context cancellation", err)
	}
}

func TestModeratorDataContextPropagatesCancellation(t *testing.T) {
	client := NewClient("https://example.test", "key", ContentModeratorV2Path)
	client.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	moderator := &ModeratorV2{Client: *client}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := moderator.EvalDataContext(ctx, []byte("image")); !errors.Is(err, context.Canceled) {
		t.Fatalf("moderator error = %v, want context cancellation", err)
	}
}
