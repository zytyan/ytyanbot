package cloudflare

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testImage(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1600, 900)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestModerationRequestAndSeverity(t *testing.T) {
	img := testImage(t)
	for _, testCase := range []struct {
		severity int
		anime    string
	}{
		{0, "yes"}, {0, "no"}, {2, "yes"}, {2, "no"}, {4, "yes"}, {4, "no"}, {6, "yes"}, {6, "no"},
	} {
		severity, anime := testCase.severity, testCase.anime
		t.Run(fmt.Sprintf("%d-%s", severity, anime), func(t *testing.T) {
			m := NewModerator(Config{AccountID: "test-account", APIToken: "test-token"})
			m.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPost || req.URL.Path != "/client/v4/accounts/test-account/ai/run/"+Model || req.Header.Get("Authorization") != "Bearer test-token" {
					t.Fatalf("unexpected provider request: %s %s", req.Method, req.URL.Path)
				}
				var body struct {
					Model  string `json:"model"`
					Images []struct {
						ContentType string `json:"content_type"`
						Base64      string `json:"base64"`
					} `json:"images"`
					Questions map[string]struct {
						Type     string            `json:"type"`
						Criteria map[string]string `json:"criteria"`
					} `json:"questions"`
				}
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.Model != "clef-flash" || len(body.Images) != 1 || body.Images[0].ContentType != "image/jpeg" || len(body.Questions) != 2 {
					t.Fatal("expected clef-flash with one image with severity and anime questions")
				}
				question := body.Questions["microsoft_severity"]
				if question.Type != "choice" || len(question.Criteria) != 4 {
					t.Fatal("missing four-level severity criteria")
				}
				for _, score := range []string{"0", "2", "4", "6"} {
					if question.Criteria[score] == "" {
						t.Fatalf("missing score %s", score)
					}
				}
				animeQuestion := body.Questions["anime_illustration"]
				if animeQuestion.Type != "choice" || len(animeQuestion.Criteria) != 2 || animeQuestion.Criteria["yes"] == "" || animeQuestion.Criteria["no"] == "" {
					t.Fatal("missing anime illustration yes/no criteria")
				}
				data, err := base64.StdEncoding.DecodeString(body.Images[0].Base64)
				if err != nil {
					t.Fatal(err)
				}
				cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				if cfg.Width != 768 || cfg.Height != 432 || len(data) > 128*1024 {
					t.Fatalf("bad prepared image: %+v, %d bytes", cfg, len(data))
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewBufferString(fmt.Sprintf(`{"success":true,"result":{"answers":{"microsoft_severity":{"type":"choice","choice":"%d"},"anime_illustration":{"type":"choice","choice":"%s"}}}}`, severity, anime)))}, nil
			})
			result, err := m.EvalDataContext(context.Background(), img)
			if err != nil || result == nil || result.Severity != severity || result.AnimeIllustration != (anime == "yes") {
				t.Fatalf("got %+v, %v; want %d", result, err, severity)
			}
		})
	}
}

func TestModerationFailuresDoNotProduceScores(t *testing.T) {
	img := testImage(t)
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"http failure", 429, `{}`},
		{"unsuccessful envelope", 200, `{"success":false,"errors":[{"code":123}]}`},
		{"missing severity", 200, `{"success":true,"result":{"answers":{}}}`},
		{"invalid severity", 200, `{"success":true,"result":{"answers":{"microsoft_severity":{"type":"choice","choice":"3"},"anime_illustration":{"type":"choice","choice":"no"}}}}`},
		{"wrong type", 200, `{"success":true,"result":{"answers":{"microsoft_severity":{"type":"score","choice":"0"},"anime_illustration":{"type":"choice","choice":"no"}}}}`},
		{"missing anime", 200, `{"success":true,"result":{"answers":{"microsoft_severity":{"type":"choice","choice":"0"}}}}`},
		{"invalid anime", 200, `{"success":true,"result":{"answers":{"microsoft_severity":{"type":"choice","choice":"0"},"anime_illustration":{"type":"choice","choice":"unknown"}}}}`},
		{"malformed response", 200, `not json`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := NewModerator(Config{AccountID: "account", APIToken: "token"})
			m.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: c.status, Body: io.NopCloser(bytes.NewBufferString(c.body))}, nil
			})
			if result, err := m.EvalDataContext(context.Background(), img); result != nil || err == nil {
				t.Fatalf("got %+v, %v; want no score and an error", result, err)
			}
		})
	}
}

func TestModerationCancellationAndInvalidImage(t *testing.T) {
	m := NewModerator(Config{AccountID: "account", APIToken: "token"})
	img := testImage(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.EvalDataContext(ctx, img); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancellation", err)
	}
	if _, err := m.EvalDataContext(context.Background(), []byte("invalid image")); err == nil {
		t.Fatal("invalid image accepted")
	}
	ctx, cancel = context.WithCancel(context.Background())
	started := make(chan struct{})
	m.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(started)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	result := make(chan error, 1)
	go func() { _, err := m.EvalDataContext(ctx, img); result <- err }()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancellation", err)
	}
}
