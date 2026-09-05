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
	// 私信是聊天语境:敲一个回车就该断行。帖子正文照 CommonMark 规则(单换行并成一段),
	// 聊天里那样处理会把用户分行写的内容挤成一坨。
	dmMD = goldmark.New(goldmark.WithRendererOptions(gmhtml.WithHardWraps()))
	// 兜底:把不可信协议的链接/图片源替换为占位,防 javascript:/data:/vbscript: 注入。
	unsafeURL = regexp.MustCompile(`(?i)((?:href|src)=")(?:javascript|vbscript|data):`)
)

// Render 把 Markdown 源渲染为可信 HTML。
// goldmark 默认把原始 HTML 当纯文本输出(不开 WithUnsafe),
// 渲染结果仍经过一次危险协议链接过滤,可直接用于 safeHTML。
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
