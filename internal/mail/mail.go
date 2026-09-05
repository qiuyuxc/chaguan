// Package mail 用 net/smtp 发纯文本邮件。只支持三种连接方式:
// starttls(587)、ssl(465,连上即 TLS)、none(明文,仅用于内网中继)。
package mail

import (
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// Config 是一次发信所需的全部配置(来自后台「邮件」设置)。
type Config struct {
	Host   string
	Port   string
	User   string
	Pass   string
	From   string
	Secure string // starttls | ssl | none
}

const dialTimeout = 12 * time.Second

// Send 发一封纯文本邮件。失败返回可直接展示给管理员的错误。
func Send(cfg Config, to, subject, body string) error {
	if cfg.Host == "" || cfg.Port == "" || cfg.From == "" {
		return errors.New("SMTP 未配置完整(需要服务器、端口、发件人)")
	}
	if strings.TrimSpace(to) == "" {
		return errors.New("收件人为空")
	}
	addr := net.JoinHostPort(cfg.Host, cfg.Port)
	client, err := dial(cfg, addr)
	if err != nil {
		return fmt.Errorf("连接 %s 失败: %w", addr, err)
	}
	defer client.Close()

	if cfg.User != "" && cfg.Pass != "" {
		if err := client.Auth(smtp.PlainAuth("", cfg.User, cfg.Pass, cfg.Host)); err != nil {
			return fmt.Errorf("SMTP 认证失败: %w", err)
		}
	}
	if err := client.Mail(cfg.From); err != nil {
		return fmt.Errorf("设置发件人失败: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("设置收件人失败: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("准备正文失败: %w", err)
	}
	if _, err := w.Write([]byte(message(cfg.From, to, subject, body))); err != nil {
		return fmt.Errorf("写入正文失败: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("提交邮件失败: %w", err)
	}
	return client.Quit()
}

// dial 按 Secure 建立连接并完成 EHLO/STARTTLS。
func dial(cfg Config, addr string) (*smtp.Client, error) {
	if cfg.Secure == "ssl" { // 465:握手即 TLS
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: dialTimeout}, "tcp", addr,
			&tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12})
		if err != nil {
			return nil, err
		}
		return smtp.NewClient(conn, cfg.Host)
	}
	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return nil, err
	}
	conn.SetDeadline(time.Now().Add(2 * dialTimeout))
	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if cfg.Secure == "starttls" { // 587:先明文握手再升级
		if err := client.StartTLS(&tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			client.Close()
			return nil, fmt.Errorf("STARTTLS 失败: %w", err)
		}
	}
	return client, nil
}

// message 组装最小可用的邮件报文。中文主题按 RFC 2047 编码,正文声明 UTF-8。
func message(from, to, subject, body string) string {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	// 正文里出现的裸 \n 统一成 \r\n,并给行首的点做转义(SMTP 数据段规则)
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")
	body = strings.ReplaceAll(body, "\r\n.", "\r\n..")
	b.WriteString(body)
	b.WriteString("\r\n")
	return b.String()
}
