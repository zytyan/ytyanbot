package ytdlp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	jsoniter "github.com/json-iterator/go"
	"github.com/tidwall/gjson"
)

var Bin = "yt-dlp"
var reA = regexp.MustCompile(`(?i)/(BV[123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz]+|av\d+)`)

type RunError struct {
	Err      error
	Stdout   string
	Stderr   string
	Args     []string
	ExitCode int
}

func (e *RunError) Error() string {
	argList := make([]string, 0, len(e.Args))
	for _, arg := range e.Args {
		argList = append(argList, strconv.Quote(arg))
	}
	args := strings.Join(argList, " ")
	return fmt.Sprintf("%s\nstderr=%s\nstdout=%s\nargs=%s", e.Err, e.Stderr, e.Stdout, args)
}

type Req struct {
	Url             string
	AudioOnly       bool
	Resolution      int64
	EmbedMetadata   bool
	PostExec        string
	PriorityFormats []string

	tmpPath  string
	prepared bool
}

type Info struct {
	Title    string
	Uploader string
	Desc     string
}

func defaultJsonString(j []byte, path, defaultStr string) string {
	if s := gjson.GetBytes(j, path).String(); s != "" {
		return s
	}
	return defaultStr
}

type Resp struct {
	req         *Req
	FilePath    string
	Info        Info
	InfoJsonErr error // 考虑到info json不是必须的，所以不返回错误，而是在这里附加上
}

func (c *Req) prepare() error {
	if c.prepared {
		return nil
	}
	c.prepared = true
	if c.Resolution == 0 {
		c.Resolution = 1080
	}
	var err error
	if c.tmpPath == "" {
		c.tmpPath, err = os.MkdirTemp("", "ytdlp-*")
		if err != nil {
			return err
		}
	}
	return nil

}

func (c *Req) args() []string {
	args := []string{c.Url,
		"-o",
		filepath.Join(c.tmpPath, "%(title).150B.%(ext)s"),
		"--quiet",
		"--print", "after_move:%(filepath)j",
		"--windows-filenames", "--write-info-json",
	}
	if strings.Contains(c.Url, "youtu") {
		// 目前就油管用到了这个能力，在B站启用反而会导致下载失败的问题
		args = append(args, "--format", `bestvideo+bestaudio/best`)
	}
	// 分辨率
	formats := strings.Join(c.PriorityFormats, ":")
	args = append(args, "--format-sort", fmt.Sprintf(`+vcodec:%s,+acodec:flac:alac:wav:aiff:aac:mp4a:mp3,res:%d`, formats, c.Resolution))
	if c.AudioOnly {
		args = append(args, "--extract-audio", "--audio-format", "mp3", "--audio-quality", "0")
	}
	if c.EmbedMetadata {
		args = append(args, "--embed-metadata", "--embed-info-json", "--add-metadata")
	}
	if c.PostExec != "" {
		args = append(args, "--exec", c.PostExec)
	}

	return args
}

func (c *Req) Clean() error {
	if c == nil {
		return nil
	}
	return os.RemoveAll(c.tmpPath)
}

func isVideoFile(name string) bool {
	videoSuffixes := []string{".mp4", ".mkv", ".flv", ".ts", ".avi", ".webm"}
	return slices.Contains(videoSuffixes, strings.ToLower(filepath.Ext(name)))

}

func getFirstFile(path string) (string, error) {
	var file string
	err := filepath.WalkDir(path, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !d.IsDir() && isVideoFile(d.Name()) {
			file = path
			return filepath.SkipAll
		}

		return nil
	})
	if err != nil {
		return "", fmt.Errorf("读取目录 %s 失败", path)
	}
	if file != "" {
		return file, nil
	}
	return "", fmt.Errorf("%s 目录中没有文件", path)
}

var client = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
	Timeout: 10 * time.Second,
}

func parseBilibiliJsonData(body io.Reader) (Info, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return Info{}, err
	}
	if code := gjson.GetBytes(data, `code`).Int(); code != 0 {
		return Info{}, fmt.Errorf("code %d != 0, error", code)
	}
	info := Info{}
	info.Title = defaultJsonString(data, `data.title`, "未知标题")
	info.Uploader = defaultJsonString(data, `data.owner.name`, "未知上传者")
	info.Desc = defaultJsonString(data, `data.desc`, "")
	info.Desc = defaultJsonString(data, `data.desc_v2.*.raw_text`, info.Desc)
	return info, nil
}

func getBilibiliVideoInfo(url string) (info Info, err error) {
	info = Info{
		Title:    "未知标题",
		Uploader: "未知上传者",
		Desc:     "-",
	}
	location := url
	if strings.Contains(location, "b23.tv") {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return info, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return info, err
		}
		location = resp.Header.Get("Location")
		_ = resp.Body.Close()
		if location == "" {
			return info, fmt.Errorf("cannot found %s", url)
		}
	}
	submatch := reA.FindStringSubmatch(location)
	if submatch == nil {
		return info, fmt.Errorf("regex cannot found %s", location)
	}
	bv := submatch[1]
	var reqUrl string
	switch bv[0] {
	case 'A', 'a':
		reqUrl = fmt.Sprintf(`https://api.bilibili.com/x/web-interface/view?aid=%s`, bv[2:])
	case 'B', 'b':
		reqUrl = fmt.Sprintf(`https://api.bilibili.com/x/web-interface/view?bvid=%s`, bv)
	default:
		return info, fmt.Errorf("localtion AV/BV not in regular %s", location)
	}
	resp, err := client.Get(reqUrl)
	if err != nil {
		return info, err
	}
	defer resp.Body.Close()
	return parseBilibiliJsonData(resp.Body)
}

func (c *Req) runWithCtxBBDown(ctx context.Context) (resp *Resp, err error) {
	resp = &Resp{req: c}
	wg := sync.WaitGroup{}
	wg.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				resp.InfoJsonErr = fmt.Errorf("get BilibiliVideoInfo error: %s", r)
			}

		}()
		resp.Info, resp.InfoJsonErr = getBilibiliVideoInfo(c.Url)
	})

	tmp := c.tmpPath
	tmp, err = filepath.Abs(tmp)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "/usr/local/bin/dotnet-tools/BBDown", c.Url, "-app", "-e", "hevc,av1,avc", "-q", "720P 高清", "-F", fmt.Sprintf("%s/<bvid>", tmp))
	outBuf := new(bytes.Buffer)
	errBuf := new(bytes.Buffer)
	cmd.Stdout = outBuf
	cmd.Stderr = errBuf
	cmd.Dir = tmp
	err = cmd.Run()
	var exitErr *exec.ExitError
	if err != nil && errors.As(err, &exitErr) {
		return resp, &RunError{
			Err:      err,
			Stdout:   outBuf.String(),
			Stderr:   errBuf.String(),
			ExitCode: exitErr.ExitCode(),
			Args:     cmd.Args,
		}
	}
	wg.Wait()
	resp.FilePath, err = getFirstFile(tmp)
	if err != nil {
		return nil, err
	}
	return resp, err
}

func (c *Req) runWithCtx(ctx context.Context) (resp *Resp, err error) {
	resp = &Resp{req: c}
	err = c.prepare()
	if err != nil {
		return resp, err
	}
	if strings.Contains(c.Url, "b23.tv") || strings.Contains(c.Url, "bilibili.com") {
		return c.runWithCtxBBDown(ctx)
	}
	args := c.args()
	cmd := exec.CommandContext(ctx, Bin, args...)
	outBuf := new(bytes.Buffer)
	errBuf := new(bytes.Buffer)
	cmd.Stdout = outBuf
	cmd.Stderr = errBuf
	err = cmd.Run()
	var exitErr *exec.ExitError
	if err != nil && errors.As(err, &exitErr) {
		return resp, &RunError{
			Err:      err,
			Stdout:   outBuf.String(),
			Stderr:   errBuf.String(),
			ExitCode: exitErr.ExitCode(),
			Args:     args,
		}
	}
	err = jsoniter.Unmarshal(outBuf.Bytes(), &resp.FilePath)
	if err != nil {
		return resp, err
	}

	ext := filepath.Ext(resp.FilePath)
	infoJsonPath := resp.FilePath[:len(resp.FilePath)-len(ext)] + ".info.json"
	infoJsonFile, err := os.ReadFile(infoJsonPath)
	if err != nil {
		resp.InfoJsonErr = err
	} else {
		resp.Info.Title = defaultJsonString(infoJsonFile, `title`, `未知标题`)
		resp.Info.Uploader = defaultJsonString(infoJsonFile, `uploader`, `未知上传者`)
		resp.Info.Desc = defaultJsonString(infoJsonFile, `description`, ``)
	}

	return resp, nil
}

func (c *Req) RunWithTimeout(timeout time.Duration) (*Resp, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return c.runWithCtx(ctx)
}
