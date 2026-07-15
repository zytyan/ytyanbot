package genbot

import (
	"fmt"
	"main/globalcfg/h"
	genai "main/handlers/genbot/geminiapi"
	"main/helpers/ent2md"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

func tgMsgToGenaiContent(bot *gotgbot.Bot, msg *gotgbot.Message) (*genai.Content, error) {
	contents := &genai.Content{}
	addPart := func(part *genai.Part) {
		contents.Parts = append(contents.Parts, part)
	}
	addTextPart := func(text string) {
		contents.Parts = append(contents.Parts, genai.NewPartFromText(text))
	}
	now := time.Now().Format("2006-01-02 15:04:05")
	header := fmt.Sprintf("%s [%s]\n", now, msg.GetSender().Name())
	addTextPart(header)
	mdTxt := ent2md.TgMsgTextToMarkdown(msg)
	if mdTxt != "" {
		addTextPart(mdTxt)
	}
	var fileId, mimeType string
	if msg.Photo != nil {
		fileId = msg.Photo[len(msg.Photo)-1].FileId
		mimeType = "image/jpeg"
		addTextPart("[photo]")
	} else if sticker := msg.Sticker; sticker != nil {
		if sticker.IsAnimated {
			addTextPart(fmt.Sprintf("[用户发送了一个%s的sticker，但无法渲染]", sticker.Emoji))
			return contents, nil
		}
		addTextPart("[sticker]")
		fileId = sticker.FileId
		mimeType = "image/webp"
		if sticker.IsVideo {
			mimeType = "video/webm"
		}
	} else if video := msg.Video; video != nil {
		if video.Duration >= 5*60 || video.FileSize > 15*1024*1024 {
			addTextPart("用户发送了一个视频，但是太长看不到")
			return contents, nil
		}
		addTextPart("[video]")
		fileId = video.FileId
		mimeType = "video/mp4"
	} else if ani := msg.Animation; ani != nil {
		if ani.Duration >= 5*60 || ani.FileSize > 15*1024*1024 {
			addTextPart("用户发送了一个视频，但是太长看不到")
			return contents, nil
		}
		addTextPart("[video]")
		fileId = ani.FileId
		mimeType = "video/mp4"
	}

	data, err := h.DownloadToMemoryCached(bot, fileId)
	if err != nil {
		return nil, err
	}
	addPart(genai.NewPartFromBytes(data, mimeType))
	return contents, nil
}
