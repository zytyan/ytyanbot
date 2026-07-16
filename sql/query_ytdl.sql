-- encoding: utf-8

-- name: GetYtDlpDbCache :one
SELECT *
FROM yt_dl_results
WHERE url = ?
  AND audio_only = ?
  AND resolution = ?;

-- name: UpdateYtDlpCache :exec
INSERT INTO yt_dl_results
(url, audio_only, resolution, file_id, title, description, uploader, upload_count)
VALUES (?, ?, ?, ?, ?, ?, ?, 1)
ON CONFLICT DO UPDATE
    SET file_id=excluded.file_id,
        title=excluded.title,
        description=excluded.description,
        uploader=excluded.uploader;

-- name: IncYtDlUploadCount :exec
UPDATE yt_dl_results
SET upload_count=upload_count + 1
WHERE file_id = ?;
