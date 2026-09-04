CREATE TABLE verify_requests (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  kind TEXT NOT NULL DEFAULT '',
  subject TEXT NOT NULL DEFAULT '',
  note TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'pending',
  created_at INTEGER NOT NULL,
  handled_at INTEGER,
  handled_by INTEGER
);
CREATE INDEX idx_verify_requests_user ON verify_requests(user_id);
CREATE INDEX idx_verify_requests_status ON verify_requests(status);
