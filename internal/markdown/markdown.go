// Package markdown 渲染帖子正文(CommonMark)。
package markdown

import (
	"bytes"
	"html"
	"regexp"

	"github.com/yuin/goldmark"
	gmhtml "github.com/yuin/goldmark/renderer/html"
)

var (
	md = goldmark.New()
	// 私信按聊天语境单换行直接断行
	dmMD = goldmark.New(goldmark.WithRendererOptions(gmhtml.WithHardWraps()))
	// 不可信协议的链接/图片源替换为占位
	unsafeURL = regexp.MustCompile(`(?i)((?:href|src)=")(?:javascript|vbscript|data):`)
)

// Render 把 Markdown 源渲染为可信 HTML(goldmark 不开 WithUnsafe,
// 原始 HTML 按纯文本输出,渲染后再过一次危险协议过滤)。
func Render(src string) string { return render(md, src) }

// RenderDM 渲染私信正文:过滤口径与帖子完全一致,只是单换行直接断行。
func RenderDM(src string) string { return render(dmMD, src) }

func render(m goldmark.Markdown, src string) string {
	var buf bytes.Buffer
	if err := m.Convert([]byte(src), &buf); err != nil {
		return "<p>" + html.EscapeString(src) + "</p>"
	}
	return unsafeURL.ReplaceAllString(buf.String(), `$1#`)
}
