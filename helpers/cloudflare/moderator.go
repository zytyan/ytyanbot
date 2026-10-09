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
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp"
)

const Model = "@cf/cloudflare/clef-flash"

// These criteria paraphrase Microsoft's Sexual IMAGE severity definitions.
// https://learn.microsoft.com/en-us/azure/ai-services/content-safety/concepts/harm-categories#image-content
var sexualSeverity = map[string]string{
	"0": "Safe: benign family or romantic imagery, everyday clothing and objects, nonsexual poses or activities, nonsexual nudity, animal reproduction, and sexual health contexts; examples include ordinary hugs and nonsexual affectionate contact.",
	"2": "Low: suggestive behavior or presentation, including fashion poses, body decoration, artistic nudity, and mildly provocative displays.",
	"4": "Medium: erotic material, commercial or amateur, where nude or partly clothed subjects appear in scenes intended to arouse, without visibly photographed penetration; includes erotic posing, less explicit artistic nudity, adult businesses, partially hidden sexual activity, backstage adult productions, and pole performances.",
	"6": "High: clearly detailed sexual anatomy or sexual acts; includes pornography, sexual solicitation, voyeuristic content, sexual acts in art, nonconsensual sexual activity, exploitation or grooming of children, incest, and sexual activity involving animals or deceased persons.",
}

type Config struct {
	AccountID string `koanf:"account-id"`
	APIToken  string `koanf:"api-token"`
}

type Moderator struct {
	cfg    Config
	client http.Client
}

type Result struct {
	Severity int
}

func NewModerator(cfg Config) *Moderator {
	return &Moderator{cfg: cfg, client: http.Client{Timeout: 30 * time.Second}}
}

// Small JPEGs avoid the API's base64 request-size/context estimation failures.
func prepareImage(data []byte) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode moderation image: %w", err)
	}
	if cfg.Width < 128 || cfg.Height < 128 {
		return nil, errors.New("image too small")
	}
	if int64(cfg.Width)*int64(cfg.Height) > 16_000_000 {
		return nil, errors.New("moderation image exceeds 16 megapixels")
	}
	img, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return nil, fmt.Errorf("decode moderation image: %w", err)
	}
	img = imaging.Fit(img, 768, 768, imaging.Lanczos)
	for _, quality := range []int{75, 60, 45, 30} {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
			return nil, err
		}
		if buf.Len() <= 128*1024 {
			return buf.Bytes(), nil
		}
	}
	return nil, errors.New("moderation JPEG exceeds request size budget")
}

func (m *Moderator) EvalDataContext(ctx context.Context, data []byte) (*Result, error) {
	if m.cfg.AccountID == "" || m.cfg.APIToken == "" {
		return nil, errors.New("Cloudflare moderation account-id and api-token are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	img, err := prepareImage(data)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{
		"model":  "clef-flash",
		"state":  "Evaluate the single attached image for content moderation.",
		"images": []map[string]string{{"content_type": "image/jpeg", "base64": base64.StdEncoding.EncodeToString(img)}},
		"questions": map[string]any{"microsoft_severity": map[string]any{
			"type":         "choice",
			"instructions": "Assess the single attached image using the Azure AI Content Safety Sexual IMAGE severity definitions below. Select exactly one severity: 0 Safe, 2 Low, 4 Medium, or 6 High. Apply the image definitions rather than text definitions. Decide from visible evidence and context, not the image source or filename.",
			"criteria":     sexualSeverity,
		}},
	})
	if err != nil {
		return nil, err
	}
	endpoint := "https://api.cloudflare.com/client/v4/accounts/" + url.PathEscape(m.cfg.AccountID) + "/ai/run/" + Model
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.cfg.APIToken)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var envelope struct {
		Success bool `json:"success"`
		Errors  []struct {
			Code int `json:"code"`
		} `json:"errors"`
		Result struct {
			Answers map[string]struct {
				Type   string `json:"type"`
				Choice string `json:"choice"`
			} `json:"answers"`
		} `json:"result"`
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Cloudflare moderation HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decode Cloudflare moderation response: %w", err)
	}
	if !envelope.Success {
		return nil, fmt.Errorf("Cloudflare moderation unsuccessful: %v", envelope.Errors)
	}
	answer := envelope.Result.Answers["microsoft_severity"]
	if answer.Type != "choice" {
		return nil, errors.New("Cloudflare moderation response missing severity choice")
	}
	var severity int
	switch answer.Choice {
	case "0":
		severity = 0
	case "2":
		severity = 2
	case "4":
		severity = 4
	case "6":
		severity = 6
	default:
		return nil, errors.New("Cloudflare moderation returned an invalid severity")
	}
	return &Result{Severity: severity}, nil
}
