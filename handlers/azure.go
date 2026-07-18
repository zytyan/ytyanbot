package handlers

import (
	"bytes"
	"context"
	"errors"
	"image"
	g "main/globalcfg"
	"main/globalcfg/h"
	"main/helpers/azure"
	"main/helpers/lrusf"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"golang.org/x/time/rate"
)

var ErrNoImage = errors.New("no image or image too small")

var ocrCache = lrusf.NewStringKeyCache[*azure.OcrResult](500, nil)
var ocrRateLimiter = rate.NewLimiter(5, 1)

func ocrMsg(bot *gotgbot.Bot, file *gotgbot.PhotoSize) (string, error) {
	logger := logD.With("file_id", file.FileId)
	res, err := ocrCache.Get(file.FileId, func() (*azure.OcrResult, error) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
		defer cancel()
		err := ocrRateLimiter.Wait(ctx)
		if err != nil {
			return nil, err
		}
		data, err := h.DownloadToMemoryCached(bot, file.FileId)
		if err != nil {
			return nil, err
		}
		return g.Ocr().OcrDataContext(ctx, data)
	})
	if err != nil {
		logger.Warn("ocr file error", "err", err)
		return "", err
	}
	return res.Text(), nil
}

var moderatorMsgCache = lrusf.NewStringKeyCache[*azure.ModeratorV2Result](500, nil)
var moderatorRateLimiter = rate.NewLimiter(5, 1)

func moderatorMsg(bot *gotgbot.Bot, file *gotgbot.PhotoSize) (*azure.ModeratorV2Result, error) {
	logger := logD.With("file_id", file.FileId)
	result, err := moderatorMsgCache.Get(file.FileId, func() (*azure.ModeratorV2Result, error) {
		data, err := h.DownloadToMemoryCached(bot, file.FileId)
		if err != nil {
			return nil, err
		}
		cfg, _, err := image.DecodeConfig(bytes.NewBuffer(data))
		if err != nil {
			return nil, err
		}
		if cfg.Width < 128 || cfg.Height < 128 {
			return nil, errors.New("image too small")
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
		defer cancel()
		err = moderatorRateLimiter.Wait(ctx)
		if err != nil {
			return nil, err
		}
		return g.Moderator().EvalDataContext(ctx, data)
	})
	if err != nil {
		logger.Warn("moderator file error", "err", err)
	}
	return result, err
}
