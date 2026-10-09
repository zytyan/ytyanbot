package azure

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	jsoniter "github.com/json-iterator/go"
)

const defaultRequestTimeout = 30 * time.Second

//goland:noinspection GoUnusedConst
const (
	OcrPath = "/computervision/imageanalysis:analyze"
)

type ResponseError struct {
	Error struct {
		Code    string `json:"code,omitempty"`
		Message string `json:"message,omitempty"`
	} `json:"error,omitempty"`
}
type Client struct {
	client   http.Client
	endpoint string
	apiKey   string
	path     string
}

func NewClient(endpoint string, apiKey string, path string) *Client {
	return &Client{
		client: http.Client{Timeout: defaultRequestTimeout},
		apiKey: apiKey, endpoint: endpoint, path: path,
	}
}

func (c *Client) reqWithAuth(ctx context.Context, method, contentType string) (*http.Request, error) {
	urlPath := fmt.Sprintf("%s%s", c.endpoint, c.path)
	request, err := http.NewRequestWithContext(ctx, method, urlPath, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Add("Ocp-Apim-Subscription-Key", c.apiKey)
	return request, nil
}

func unmarshalResponse(resp *http.Response, v any) error {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP Response error(%d): %s", resp.StatusCode, body)
	}
	err = jsoniter.Unmarshal(body, v)
	if err != nil {
		return err
	}
	return nil
}

type Ocr struct {
	Client
	ApiVer   string
	Language string
	Features string
}

type OcrResult struct {
	ResponseError

	ModelVersion string `json:"modelVersion,omitempty"`
	Metadata     struct {
		Width  int `json:"width,omitempty"`
		Height int `json:"height,omitempty"`
	} `json:"metadata,omitempty"`
	ReadResult struct {
		Blocks []struct {
			Lines []struct {
				Text            string `json:"text,omitempty"`
				BoundingPolygon []struct {
					X int `json:"x,omitempty"`
					Y int `json:"y,omitempty"`
				} `json:"boundingPolygon,omitempty"`
				Words []struct {
					Text            string `json:"text,omitempty"`
					BoundingPolygon []struct {
						X int `json:"x,omitempty"`
						Y int `json:"y,omitempty"`
					} `json:"boundingPolygon,omitempty"`
					Confidence float64 `json:"confidence,omitempty"`
				} `json:"words,omitempty"`
			} `json:"lines,omitempty"`
		} `json:"blocks,omitempty"`
	} `json:"readResult,omitempty"`
}

func (o *Ocr) OcrDataContext(ctx context.Context, data []byte) (*OcrResult, error) {
	return o.ocr(ctx, io.NopCloser(bytes.NewReader(data)), int64(len(data)))
}

func (o *Ocr) ocr(ctx context.Context, body io.ReadCloser, contentLength int64) (*OcrResult, error) {
	defer body.Close()
	req, err := o.reqWithAuth(ctx, http.MethodPost, "image/jpeg")
	if err != nil {
		return nil, err
	}
	req.Body = body
	req.ContentLength = contentLength
	q := req.URL.Query()
	q.Add("api-version", o.ApiVer)
	if o.Features != "" {
		q.Add("features", o.Features)
	}
	if o.Language != "" {
		q.Add("language", o.Language)
	}
	req.URL.RawQuery = q.Encode()
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	res := &OcrResult{}
	err = unmarshalResponse(resp, res)
	return res, err
}

func (r *ResponseError) HasError() bool {
	return r.Error.Code == "" || r.Error.Code == "0"
}
func (r *ResponseError) ToError() error {
	if r.HasError() {
		return nil
	}
	return fmt.Errorf("azure error, code = %s, msg = %s", r.Error.Code, r.Error.Message)
}
func (r *OcrResult) Text() string {
	buf := strings.Builder{}
	for _, block := range r.ReadResult.Blocks {
		for _, line := range block.Lines {
			buf.WriteString(line.Text)
			buf.WriteByte('\n')
		}
		buf.WriteString("\n\n")
	}
	return buf.String()

}
