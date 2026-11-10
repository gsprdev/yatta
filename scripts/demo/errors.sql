-- Bad states, layered on seed.sql by `seed.sh --errors`. Each one reworks
-- pending entries from the golden path, so the day totals stay the same.
-- Day ranks fix the template: rank 1 = template 2, rank 2 = template 3.

BEGIN;

-- Failed: time on a version, which Redmine refuses. The error is the one the
-- adapter returns, as a previous upload attempt would have left it.
UPDATE time_entries SET
	task_id = (SELECT id FROM tk WHERE key = 'version:1'),
	upload_error = 'Redmine records time on issues or projects, not on a version: choose an issue'
WHERE task_id = (SELECT id FROM tk WHERE key = 'issue:7')
  AND date(start, 'unixepoch', 'localtime') = (SELECT day FROM day WHERE rank = 2);

-- Will fail: also on a version, not yet attempted, to watch a live failure.
UPDATE time_entries SET task_id = (SELECT id FROM tk WHERE key = 'version:2')
WHERE task_id = (SELECT id FROM tk WHERE key = 'issue:8')
  AND date(start, 'unixepoch', 'localtime') = (SELECT day FROM day WHERE rank = 1);

-- Departed: the IE11 ticket was rejected upstream after the last fetch. It
-- keeps its uploaded history and has pending entries to reassociate.
UPDATE rm.issues SET
	status_id = (SELECT id FROM rm.issue_statuses WHERE name = 'Rejected'),
	closed_on = datetime('now', '-1 day'), updated_on = datetime('now', '-1 day')
WHERE id = 16;
UPDATE tasks SET departed_at = unixepoch('now', '-1 day') WHERE native_id = 'issue:16';

-- Unassigned: a timer stopped before a task was chosen.
UPDATE time_entries SET task_id = NULL
WHERE date(start, 'unixepoch', 'localtime') = (SELECT day FROM day WHERE rank = 1)
  AND note IN ('Phone screen', 'Replies');

COMMIT;
