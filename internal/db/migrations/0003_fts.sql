-- FTS5 trigram 全文索引:thread_docs 每行 = 主题标题或一条正文,
-- 由 threads/posts 触发器增量维护,搜索时替代 LIKE 大表扫。
-- trigram 词元至少 3 字符,短查询由 db.SearchThreads 回退 LIKE。

CREATE VIRTUAL TABLE IF NOT EXISTS thread_docs USING fts5(
  thread_id UNINDEXED,
  post_id   UNINDEXED,
  is_title  UNINDEXED,
  text,
  tokenize = 'trigram'
);

CREATE TRIGGER IF NOT EXISTS trg_threads_docs_ai AFTER INSERT ON threads BEGIN
  INSERT INTO thread_docs(thread_id, post_id, is_title, text)
  VALUES (NEW.id, NULL, 1, NEW.title);
END;

CREATE TRIGGER IF NOT EXISTS trg_threads_docs_au AFTER UPDATE OF title ON threads BEGIN
  UPDATE thread_docs SET text = NEW.title
  WHERE thread_id = NEW.id AND is_title = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_threads_docs_ad AFTER DELETE ON threads BEGIN
  DELETE FROM thread_docs WHERE thread_id = OLD.id;
END;

CREATE TRIGGER IF NOT EXISTS trg_posts_docs_ai AFTER INSERT ON posts BEGIN
  INSERT INTO thread_docs(thread_id, post_id, is_title, text)
  VALUES (NEW.thread_id, NEW.id, 0, NEW.content_md);
END;

CREATE TRIGGER IF NOT EXISTS trg_posts_docs_au AFTER UPDATE OF content_md ON posts BEGIN
  UPDATE thread_docs SET text = NEW.content_md WHERE post_id = NEW.id;
END;

CREATE TRIGGER IF NOT EXISTS trg_posts_docs_ad AFTER DELETE ON posts BEGIN
  DELETE FROM thread_docs WHERE post_id = OLD.id;
END;

-- 存量回填(直接写 FTS 表,不会触发上面的 base 表触发器)
INSERT INTO thread_docs(thread_id, post_id, is_title, text)
SELECT id, NULL, 1, title FROM threads;

INSERT INTO thread_docs(thread_id, post_id, is_title, text)
SELECT thread_id, id, 0, content_md FROM posts;
