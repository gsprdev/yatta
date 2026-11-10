-- Golden-path demo data, written to yatta (main) and Redmine (rm) in one pass
-- so each uploaded record's remote_id is the Redmine time entry it produced.
-- Run by seed.sh, which attaches rm and sets @token and @url.
--
-- The uploaded records mirror core.Group under the seeded policy (quarter
-- hour, nearest, 15 min minimum excluded, per task and day). If that logic
-- changes, the history here stays as it was, like a real upload would.

PRAGMA foreign_keys = ON;
BEGIN;
PRAGMA defer_foreign_keys = ON;

------------------------------------------------------------------ Redmine

UPDATE rm.users SET firstname = 'Alex', lastname = 'Rivera', must_change_passwd = 0 WHERE id = 1;
INSERT INTO rm.tokens (user_id, action, value, created_on, updated_on)
VALUES (1, 'api', @token, datetime('now'), datetime('now'));
DELETE FROM rm.settings WHERE name = 'rest_api_enabled';
INSERT INTO rm.settings (name, value, updated_on) VALUES ('rest_api_enabled', '1', datetime('now'));
-- yatta sends no activity, so Redmine needs a default one.
UPDATE rm.enumerations SET is_default = (name = 'Development') WHERE type = 'TimeEntryActivity';

INSERT INTO rm.projects (id, name, identifier, description, parent_id, lft, rgt, is_public, status, created_on, updated_on) VALUES
	(1, 'Acme Website', 'acme-web',    'Customer storefront',          NULL, 1, 4, 1, 1, datetime('now', '-90 days'), datetime('now', '-90 days')),
	(2, 'Acme Blog',    'acme-blog',   'Company blog',                 1,    2, 3, 1, 1, datetime('now', '-90 days'), datetime('now', '-90 days')),
	(3, 'Mobile App',   'acme-mobile', 'iOS and Android apps',         NULL, 5, 6, 1, 1, datetime('now', '-90 days'), datetime('now', '-90 days')),
	(4, 'Internal Ops', 'ops',         'IT, hiring, team rituals',     NULL, 7, 8, 1, 1, datetime('now', '-90 days'), datetime('now', '-90 days'));
INSERT INTO rm.enabled_modules (project_id, name)
SELECT p.id, m.name FROM rm.projects p, (SELECT 'issue_tracking' AS name UNION ALL SELECT 'time_tracking') m;
INSERT INTO rm.projects_trackers (project_id, tracker_id) SELECT p.id, t.id FROM rm.projects p, rm.trackers t;

INSERT INTO rm.versions (id, project_id, name, status, sharing, effective_date, created_on, updated_on) VALUES
	(1, 1, '1.0', 'open', 'none', date('now', '+14 days'), datetime('now', '-60 days'), datetime('now', '-60 days')),
	(2, 1, '1.1', 'open', 'none', date('now', '+45 days'), datetime('now', '-60 days'), datetime('now', '-60 days')),
	(3, 3, '2.0', 'open', 'none', date('now', '+30 days'), datetime('now', '-60 days'), datetime('now', '-60 days'));

-- id, project, tracker, status, subject, version, parent, root, lft, rgt, assignee
INSERT INTO rm.issues (id, project_id, tracker_id, status_id, subject, fixed_version_id, parent_id, root_id, lft, rgt,
	assigned_to_id, priority_id, author_id, created_on, updated_on, start_date)
SELECT i.column1, i.column2, t.id, s.id, i.column5, i.column6, i.column7, i.column8, i.column9, i.column10, i.column11,
	(SELECT id FROM rm.enumerations WHERE type = 'IssuePriority' AND is_default),
	1, datetime('now', '-30 days'), datetime('now', '-2 days'), date('now', '-30 days')
FROM (VALUES
	(1,  1, 'Feature', 'In Progress', 'Checkout redesign',            1,    NULL, 1,  1, 6, 1),
	(2,  1, 'Feature', 'In Progress', 'Payment form layout',          1,    1,    1,  2, 3, 1),
	(3,  1, 'Feature', 'New',         'Address autocomplete',         1,    1,    1,  4, 5, 1),
	(4,  1, 'Bug',     'In Progress', 'Cart total rounding bug',      1,    NULL, 4,  1, 2, 1),
	(5,  1, 'Feature', 'New',         'Wishlist',                     2,    NULL, 5,  1, 2, 1),
	(6,  1, 'Bug',     'Feedback',    'Footer links broken',          NULL, NULL, 6,  1, 2, 1),
	(7,  1, 'Bug',     'In Progress', 'Search results pagination',    NULL, NULL, 7,  1, 2, 1),
	(8,  2, 'Feature', 'In Progress', 'RSS feed',                     NULL, NULL, 8,  1, 2, 1),
	(9,  2, 'Feature', 'New',         'Author bio pages',             NULL, NULL, 9,  1, 2, 1),
	(10, 3, 'Feature', 'In Progress', 'Push notification opt-in',     3,    NULL, 10, 1, 2, 1),
	(11, 3, 'Bug',     'In Progress', 'Crash on Android 15 launch',   3,    NULL, 11, 1, 2, 1),
	(12, 3, 'Feature', 'New',         'Offline mode for orders',      NULL, NULL, 12, 1, 2, 1),
	(13, 4, 'Support', 'In Progress', 'Daily standup',                NULL, NULL, 13, 1, 2, 1),
	(14, 4, 'Support', 'New',         'Laptop setup for new hire',    NULL, NULL, 14, 1, 2, 1),
	(15, 4, 'Support', 'New',         'Rotate TLS certificates',      NULL, NULL, 15, 1, 2, NULL),
	(16, 1, 'Bug',     'In Progress', 'Legacy IE11 support',          NULL, NULL, 16, 1, 2, 1),
	(17, 4, 'Support', 'In Progress', 'Code review rotation',         NULL, NULL, 17, 1, 2, 1)
) i
JOIN rm.trackers t ON t.name = i.column3
JOIN rm.issue_statuses s ON s.name = i.column4;

-------------------------------------------------------------------- yatta

INSERT INTO settings (key, value) VALUES
	('rounding_increment', '"quarter"'),
	('rounding_direction', '"nearest"'),
	('minimum_duration_s', '900'),
	('below_minimum_behavior', '"exclude"'),
	('rounding_aggregate', '"task_day"');
INSERT INTO integration_config (id, integration, base_url, keyring_key, task_query, last_fetch_at)
VALUES (1, 'redmine', @url, 'redmine-demo', NULL, unixepoch());

-- Remote tasks, as Redmine.FetchTasks would return them under the default
-- query (open issues assigned to me), in fetch order.
CREATE TEMP TABLE listed AS
SELECT i.* FROM rm.issues i JOIN rm.issue_statuses s ON s.id = i.status_id
WHERE NOT s.is_closed AND i.assigned_to_id = 1;

CREATE TEMP TABLE fetched (ord INTEGER PRIMARY KEY, native TEXT, parent TEXT, name TEXT, label TEXT, node_type TEXT);
INSERT INTO fetched (native, parent, name, label, node_type)
SELECT 'project:' || id, 'project:' || parent_id, name, identifier, 'project' FROM rm.projects ORDER BY id;
INSERT INTO fetched (native, parent, name, label, node_type)
SELECT 'version:' || v.id, 'project:' || min(l.project_id), v.name, NULL, 'version'
FROM listed l JOIN rm.versions v ON v.id = l.fixed_version_id GROUP BY v.id ORDER BY max(l.id) DESC;
INSERT INTO fetched (native, parent, name, label, node_type)
SELECT 'issue:' || l.id,
	CASE WHEN l.parent_id IN (SELECT id FROM listed) THEN 'issue:' || l.parent_id
	     WHEN l.fixed_version_id IS NOT NULL THEN 'version:' || l.fixed_version_id
	     ELSE 'project:' || l.project_id END,
	l.subject, '#' || l.id, lower(t.name)
FROM listed l JOIN rm.trackers t ON t.id = l.tracker_id ORDER BY l.id DESC;

-- Local tasks: key, name, parent key, order, archived.
CREATE TEMP TABLE local (key TEXT, name TEXT, parent TEXT, ord INTEGER, archived INTEGER);
INSERT INTO local VALUES
	('Admin',                'Admin',                NULL,       0, 0),
	('Email & Slack',        'Email & Slack',        'Admin',    0, 0),
	('Planning',             'Planning',             'Admin',    1, 0),
	('Learning',             'Learning',             NULL,       1, 0),
	('Go reading group',     'Go reading group',     'Learning', 0, 0),
	('Interviews',           'Interviews',           NULL,       2, 0),
	('Conference talk prep', 'Conference talk prep', NULL,       3, 1);

-- Every task's local id, by native id or local name.
CREATE TEMP TABLE tk (key TEXT PRIMARY KEY, id TEXT NOT NULL DEFAULT (lower(hex(randomblob(16)))));
INSERT INTO tk (key) SELECT native FROM fetched UNION ALL SELECT key FROM local;

INSERT INTO tasks (id, name, parent_id, sort_order, integration, native_id, label, node_type, fetched_at, created_at)
SELECT k.id, f.name, pk.id, f.ord - 1, 'redmine', f.native, f.label, f.node_type, unixepoch(), unixepoch('now', '-30 days')
FROM fetched f JOIN tk k ON k.key = f.native LEFT JOIN tk pk ON pk.key = f.parent;
INSERT INTO tasks (id, name, parent_id, sort_order, archived_at, created_at)
SELECT k.id, l.name, pk.id, l.ord, CASE WHEN l.archived THEN unixepoch('now', '-20 days') END, unixepoch('now', '-60 days')
FROM local l JOIN tk k ON k.key = l.key LEFT JOIN tk pk ON pk.key = l.parent;

-- Days: today is rank 0, then the previous 10 working days.
CREATE TEMP TABLE day AS
WITH RECURSIVE d(n, day) AS (
	SELECT 1, date('now', 'localtime', '-1 day')
	UNION ALL SELECT n + 1, date(day, '-1 day') FROM d WHERE n < 20)
SELECT date('now', 'localtime') AS day, 0 AS rank
UNION ALL
SELECT day, rank FROM (SELECT day, row_number() OVER (ORDER BY day DESC) AS rank
                       FROM d WHERE strftime('%w', day) NOT IN ('0', '6'))
WHERE rank <= 10;

-- Five day templates, used in turn: template = rank % 5 + 1. Within a day a
-- ticket may recur with the same note or different ones; a lone short entry
-- falls under the minimum and is excluded at upload.
CREATE TEMP TABLE tpl (t INTEGER, at TEXT, mins INTEGER, task TEXT NOT NULL REFERENCES tk(key), note TEXT);
INSERT INTO tpl VALUES
	(1, '08:30', 25, 'Email & Slack',    'Inbox triage'),
	(1, '09:00', 15, 'issue:13',         'Standup'),
	(1, '09:15', 60, 'issue:2',          'Card field validation'),
	(1, '10:15', 15, 'Email & Slack',    'Inbox triage'),
	(1, '10:30', 45, 'issue:2',          'Card field validation'),
	(1, '11:15', 30, 'issue:4',          'Reproduce with a three-item cart'),
	(1, '11:45', 45, 'Planning',         'Sprint planning'),
	(1, '13:00', 50, 'issue:4',          'Fix rounding; add regression test'),
	(1, '13:50',  5, 'issue:6',          'Triage'),
	(1, '13:55', 35, 'issue:17',         'Reviewed PR #412'),
	(1, '14:30', 60, 'issue:3',          'Spike: places API'),
	(1, '15:30', 20, 'Email & Slack',    'Replies'),
	(1, '15:50', 40, 'issue:3',          'Spike: places API'),
	(1, '16:30', 30, 'issue:17',         'Reviewed PR #415'),

	(2, '08:30', 30, 'Email & Slack',    'Inbox triage'),
	(2, '09:00', 15, 'issue:13',         'Standup'),
	(2, '09:15', 75, 'issue:5',          'Wishlist data model'),
	(2, '10:30', 30, 'Interviews',       'Phone screen'),
	(2, '11:00', 60, 'issue:5',          'Wishlist API endpoints'),
	(2, '13:00', 45, 'issue:11',         'Reproduce on Pixel emulator'),
	(2, '13:45', 10, 'Email & Slack',    'Replies'),
	(2, '13:55', 50, 'issue:11',         'Reproduce on Pixel emulator'),
	(2, '14:45', 25, 'project:4',        'Ops sync'),
	(2, '15:10', 35, 'issue:8',          'Feed template'),
	(2, '15:45', 20, 'issue:17',         'Reviewed PR #418'),
	(2, '16:05', 40, 'issue:8',          'Validate against W3C'),
	(2, '16:45', 15, 'Email & Slack',    'End of day wrap-up'),
	(2, '17:00', 20, 'Planning',         'Tomorrow''s priorities'),

	(3, '08:30', 30, 'Email & Slack',    'Inbox triage'),
	(3, '09:00', 15, 'issue:13',         'Standup'),
	(3, '09:15', 90, 'issue:1',          'Checkout flow review with design'),
	(3, '10:45', 15, 'Email & Slack',    'Replies'),
	(3, '11:00', 45, 'issue:16',         'Polyfill audit'),
	(3, '11:45', 30, 'Go reading group', 'Concurrency patterns, ch. 4'),
	(3, '13:00', 60, 'issue:7',          'Off-by-one on last page'),
	(3, '14:00', 20, 'issue:17',         'Reviewed PR #421'),
	(3, '14:20', 40, 'issue:7',          'Off-by-one on last page'),
	(3, '15:00', 30, 'issue:14',         'Provision laptop'),
	(3, '15:30', 45, 'issue:16',         'Polyfill audit'),
	(3, '16:15', 25, 'issue:14',         'Accounts and VPN access'),
	(3, '16:40', 20, 'Email & Slack',    'Replies'),
	(3, '17:00', 10, 'issue:9',          'Quick look at mockups'),

	(4, '08:30', 20, 'Email & Slack',    'Inbox triage'),
	(4, '08:50', 10, 'Planning',         'Review board'),
	(4, '09:00', 15, 'issue:13',         'Standup'),
	(4, '09:15', 60, 'issue:10',         'Permission prompt copy and flow'),
	(4, '10:15', 45, 'issue:12',         'Sync queue design'),
	(4, '11:00', 30, 'Interviews',       'Onsite: system design'),
	(4, '11:30', 30, 'Interviews',       'Onsite debrief'),
	(4, '13:00', 60, 'issue:12',         'Sync queue design'),
	(4, '14:00', 15, 'Email & Slack',    'Replies'),
	(4, '14:15', 45, 'issue:10',         'Wire up opt-in toggle'),
	(4, '15:00', 25, 'issue:17',         'Reviewed PR #425'),
	(4, '15:25', 35, 'issue:9',          'Author bio template'),
	(4, '16:00',  5, 'issue:6',          'Checked again, still waiting on content'),
	(4, '16:05', 40, 'issue:9',          'Author bio template'),
	(4, '16:45', 15, 'Email & Slack',    'End of day wrap-up'),

	(5, '08:30', 30, 'Email & Slack',    'Inbox triage'),
	(5, '09:00', 15, 'issue:13',         'Standup'),
	(5, '09:15', 45, 'issue:4',          'Code review feedback'),
	(5, '10:00', 60, 'issue:6',          'Update link targets'),
	(5, '11:00', 30, 'project:4',        'Ops sync'),
	(5, '11:30', 30, 'Go reading group', 'Generics deep dive'),
	(5, '13:00', 45, 'issue:2',          'Card field validation'),
	(5, '13:45', 15, 'Email & Slack',    'Replies'),
	(5, '14:00', 45, 'issue:2',          '3-D Secure redirect'),
	(5, '14:45', 30, 'issue:17',         'Reviewed PR #430'),
	(5, '15:15', 45, 'issue:16',         'Drop IE11 from browserslist'),
	(5, '16:00', 30, 'Planning',         'Weekly report'),
	(5, '16:30', 20, 'issue:5',          'Wishlist share link'),
	(5, '16:50', 10, 'Email & Slack',    'End of day wrap-up');

-- Today keeps only what finished before the timer started.
INSERT INTO time_entries (id, start, duration_s, task_id, note, created_at, updated_at)
SELECT lower(hex(randomblob(16))), s, mins * 60, k.id, note, s + mins * 60, s + mins * 60
FROM (SELECT d.rank, p.*, unixepoch(d.day || ' ' || p.at, 'utc') AS s
      FROM day d JOIN tpl p ON p.t = d.rank % 5 + 1) e
JOIN tk k ON k.key = e.task
WHERE e.rank > 0 OR e.s + e.mins * 60 <= unixepoch() - 1200;

INSERT INTO active_timer (id, start, task_id, note)
SELECT 1, max(coalesce(max(start + duration_s), 0), unixepoch() - 2700, unixepoch(date('now', 'localtime'), 'utc')),
	(SELECT id FROM tk WHERE key = 'issue:1'), 'Pairing on checkout edge cases'
FROM time_entries WHERE start >= unixepoch(date('now', 'localtime'), 'utc');

-- Uploads: every Redmine entry on days ranked 4 and older, grouped per task
-- and day, rounded to the nearest quarter hour, under 15 minutes excluded.
CREATE TEMP TABLE unit AS
WITH e AS (
	SELECT te.task_id, te.start, te.duration_s, trim(coalesce(te.note, '')) AS note,
		date(te.start, 'unixepoch', 'localtime') AS day
	FROM time_entries te JOIN tasks t ON t.id = te.task_id
	WHERE t.integration = 'redmine' AND te.start < unixepoch((SELECT day FROM day WHERE rank = 3), 'utc')),
n AS (SELECT task_id, day, note, min(start) AS first FROM e WHERE note <> '' GROUP BY 1, 2, 3),
c AS (  -- core.CombineNotes: distinct, in order, joined as sentences
	SELECT task_id, day, group_concat(CASE WHEN last OR note LIKE '%.' THEN note ELSE note || '.' END, ' ' ORDER BY first) AS note
	FROM (SELECT *, row_number() OVER (PARTITION BY task_id, day ORDER BY first DESC) = 1 AS last FROM n)
	GROUP BY 1, 2),
g AS (SELECT task_id, day, min(start) AS start, (sum(duration_s) + 450) / 900 * 900 AS dur FROM e GROUP BY 1, 2)
SELECT lower(hex(randomblob(16))) AS id, g.*, c.note,
	CASE WHEN g.dur >= 900 THEN row_number() OVER (PARTITION BY g.dur >= 900 ORDER BY g.start, g.task_id) END AS remote_id,
	unixepoch(g.day || ' 17:30', 'utc') AS uploaded_at
FROM g LEFT JOIN c USING (task_id, day);

INSERT INTO remote_records (id, remote_id, integration, target_task_id, uploaded_start, uploaded_duration_s, note, created_at)
SELECT id, CAST(remote_id AS TEXT), 'redmine', task_id, start, dur, note, uploaded_at FROM unit;

UPDATE time_entries SET remote_record_id = u.id FROM unit u
WHERE u.task_id = time_entries.task_id AND u.day = date(time_entries.start, 'unixepoch', 'localtime');

INSERT INTO rm.time_entries (id, project_id, user_id, author_id, issue_id, hours, comments, activity_id,
	spent_on, tyear, tmonth, tweek, created_on, updated_on)
SELECT u.remote_id, coalesce(i.project_id, p.id), 1, 1, i.id, u.dur / 3600.0, u.note,
	(SELECT id FROM rm.enumerations WHERE type = 'TimeEntryActivity' AND name = 'Development'),
	u.day, strftime('%Y', u.day) + 0, strftime('%m', u.day) + 0,
	(strftime('%j', date(u.day, '-' || ((strftime('%w', u.day) + 6) % 7) || ' days', '+3 days')) - 1) / 7 + 1, -- ISO week
	datetime(u.uploaded_at, 'unixepoch'), datetime(u.uploaded_at, 'unixepoch')
FROM unit u JOIN tasks t ON t.id = u.task_id
LEFT JOIN rm.issues i ON t.native_id = 'issue:' || i.id
LEFT JOIN rm.projects p ON t.native_id = 'project:' || p.id
WHERE u.remote_id IS NOT NULL;

COMMIT;
