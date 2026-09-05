#!/usr/bin/env python3
"""极简假 SMTP 服务器:只为本地验证发信链路,把收到的邮件原文追加写入文件。
用法: python3 .smoke/fakesmtp.py <port> <outfile>
"""
import socket, sys, threading

port = int(sys.argv[1])
out = sys.argv[2]
lock = threading.Lock()


def handle(conn):
    f = conn.makefile("rwb", buffering=0)
    f.write(b"220 fake.local ESMTP ready\r\n")
    data_mode = False
    body = []
    while True:
        line = f.readline()
        if not line:
            break
        if data_mode:
            if line.strip() == b".":
                data_mode = False
                with lock:
                    with open(out, "a", encoding="utf-8", errors="replace") as fh:
                        fh.write(b"".join(body).decode("utf-8", "replace"))
                        fh.write("\n===MAIL-END===\n")
                body = []
                f.write(b"250 OK queued\r\n")
            else:
                body.append(line)
            continue
        cmd = line.strip().upper()
        if cmd.startswith(b"EHLO") or cmd.startswith(b"HELO"):
            f.write(b"250 fake.local\r\n")
        elif cmd.startswith(b"MAIL FROM") or cmd.startswith(b"RCPT TO"):
            f.write(b"250 OK\r\n")
        elif cmd == b"DATA":
            data_mode = True
            f.write(b"354 End data with <CR><LF>.<CR><LF>\r\n")
        elif cmd == b"QUIT":
            f.write(b"221 Bye\r\n")
            break
        elif cmd == b"RSET":
            f.write(b"250 OK\r\n")
        else:
            f.write(b"250 OK\r\n")
    conn.close()


srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind(("127.0.0.1", port))
srv.listen(8)
print(f"fake smtp on 127.0.0.1:{port} -> {out}", flush=True)
while True:
    conn, _ = srv.accept()
    threading.Thread(target=handle, args=(conn,), daemon=True).start()
