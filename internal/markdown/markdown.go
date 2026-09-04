// Package markdown 渲染帖子正文(CommonMark)。
package markdown

import (
	"bytes"
	"html"
	"regexp"

	"github.com/yuin/goldmark"
)

var (
	md = goldmark.New()
	// 兜底:把不可信协议的链接/图片源替换为占位,防 javascript:/data:/vbscript: 注入。
	unsafeURL = regexp.MustCompile(`(?i)((?:href|src)=")(?:javascript|vbscript|data):`)
)

// Render 把 Markdown 源渲染为可信 HTML。
// goldmark 默认把原始 HTML 当纯文本输出(不开 WithUnsafe),
// 渲染结果仍经过一次危险协议链接过滤,可直接用于 safeHTML。
func Render(src string) string {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return "<p>" + html.EscapeString(src) + "</p>"
	}
	return unsafeURL.ReplaceAllString(buf.String(), `$1#`)
}
