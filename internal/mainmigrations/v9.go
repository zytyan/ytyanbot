package mainmigrations

import (
	"context"
	"database/sql"
	"fmt"
)

type invalidRatingSummary struct {
	count  int64
	values string
}

func queryInvalidRatings(ctx context.Context, tx *sql.Tx, query string) (invalidRatingSummary, error) {
	var summary invalidRatingSummary
	err := tx.QueryRowContext(ctx, query).Scan(&summary.count, &summary.values)
	return summary, err
}

func migratePictureRatingConstraints(ctx context.Context, tx *sql.Tx) error {
	botRates, err := queryInvalidRatings(ctx, tx, `SELECT COUNT(*), COALESCE(GROUP_CONCAT(DISTINCT bot_rate), '')
FROM saved_pics WHERE bot_rate NOT BETWEEN -1 AND 7`)
	if err != nil {
		return err
	}
	userRatings, err := queryInvalidRatings(ctx, tx, `SELECT COUNT(*), COALESCE(GROUP_CONCAT(DISTINCT rating), '')
FROM saved_pics_rating WHERE rating NOT BETWEEN 0 AND 7`)
	if err != nil {
		return err
	}
	if botRates.count > 0 || userRatings.count > 0 {
		return fmt.Errorf("picture rating audit failed: invalid bot_rate rows=%d values=[%s]; invalid rating rows=%d values=[%s]",
			botRates.count, botRates.values, userRatings.count, userRatings.values)
	}
	if _, err = tx.ExecContext(ctx, pictureRatingV9DDL); err != nil {
		return err
	}
	for name, query := range map[string]string{
		"saved pictures": `SELECT
(SELECT COUNT(*) FROM (
  SELECT file_uid,file_id,bot_rate,rand_key,user_rate,user_rating_sum,rate_user_count FROM saved_pics_v8
  EXCEPT
  SELECT file_uid,file_id,bot_rate,rand_key,user_rate,user_rating_sum,rate_user_count FROM saved_pics))
+
(SELECT COUNT(*) FROM (
  SELECT file_uid,file_id,bot_rate,rand_key,user_rate,user_rating_sum,rate_user_count FROM saved_pics
  EXCEPT
  SELECT file_uid,file_id,bot_rate,rand_key,user_rate,user_rating_sum,rate_user_count FROM saved_pics_v8))`,
		"picture ratings": `SELECT
(SELECT COUNT(*) FROM (
  SELECT file_uid,user_id,rating FROM saved_pics_rating_v8
  EXCEPT
  SELECT file_uid,user_id,rating FROM saved_pics_rating))
+
(SELECT COUNT(*) FROM (
  SELECT file_uid,user_id,rating FROM saved_pics_rating
  EXCEPT
  SELECT file_uid,user_id,rating FROM saved_pics_rating_v8))`,
	} {
		var differences int64
		if err = tx.QueryRowContext(ctx, query).Scan(&differences); err != nil {
			return err
		}
		if differences != 0 {
			return fmt.Errorf("%s differ after picture rating rebuild: %d rows", name, differences)
		}
	}
	_, err = tx.ExecContext(ctx, `
DROP TABLE saved_pics_rating_v8;
DROP TABLE saved_pics_v8;
`+pictureRatingV9Triggers)
	return err
}

const pictureRatingV9DDL = `
DROP TRIGGER IF EXISTS saved_pics_rating_insert_trigger;
DROP TRIGGER IF EXISTS saved_pics_rating_update_trigger;
DROP TRIGGER IF EXISTS saved_pics_rating_delete_trigger;
DROP TRIGGER IF EXISTS saved_pics_update_trigger;
DROP TRIGGER IF EXISTS saved_pics_insert_trigger;
DROP TRIGGER IF EXISTS saved_pics_delete_trigger;

ALTER TABLE saved_pics_rating RENAME TO saved_pics_rating_v8;
ALTER TABLE saved_pics RENAME TO saved_pics_v8;

CREATE TABLE saved_pics
(
    file_uid TEXT NOT NULL,
    file_id TEXT NOT NULL,
    bot_rate INTEGER NOT NULL CHECK (bot_rate BETWEEN -1 AND 7),
    rand_key INTEGER NOT NULL,
    user_rate INTEGER NOT NULL GENERATED ALWAYS AS (
        CASE WHEN rate_user_count > 0
             THEN CAST(ROUND(user_rating_sum * 1.0 / rate_user_count) AS INTEGER)
             ELSE bot_rate END
    ) STORED,
    user_rating_sum INTEGER NOT NULL DEFAULT 0,
    rate_user_count INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (file_uid),
    UNIQUE (user_rate, rand_key),
    UNIQUE (rand_key)
) WITHOUT ROWID, STRICT;

CREATE TABLE saved_pics_rating
(
    file_uid TEXT NOT NULL,
    user_id INTEGER NOT NULL,
    rating INTEGER NOT NULL DEFAULT 0 CHECK (rating BETWEEN 0 AND 7),
    PRIMARY KEY (file_uid, user_id),
    FOREIGN KEY (file_uid) REFERENCES saved_pics(file_uid) ON DELETE CASCADE
) WITHOUT ROWID, STRICT;

INSERT INTO saved_pics(file_uid,file_id,bot_rate,rand_key,user_rating_sum,rate_user_count)
SELECT file_uid,file_id,bot_rate,rand_key,user_rating_sum,rate_user_count FROM saved_pics_v8;
INSERT INTO saved_pics_rating(file_uid,user_id,rating)
SELECT file_uid,user_id,rating FROM saved_pics_rating_v8;
`

const pictureRatingV9Triggers = `
CREATE TRIGGER saved_pics_rating_insert_trigger AFTER INSERT ON saved_pics_rating
BEGIN
    UPDATE saved_pics SET user_rating_sum=user_rating_sum+new.rating,
        rate_user_count=rate_user_count+1 WHERE file_uid=new.file_uid;
END;
CREATE TRIGGER saved_pics_rating_update_trigger AFTER UPDATE ON saved_pics_rating
BEGIN
    UPDATE saved_pics SET user_rating_sum=user_rating_sum-old.rating+new.rating
    WHERE file_uid=old.file_uid;
END;
CREATE TRIGGER saved_pics_rating_delete_trigger AFTER DELETE ON saved_pics_rating
BEGIN
    UPDATE saved_pics SET user_rating_sum=user_rating_sum-old.rating,
        rate_user_count=rate_user_count-1 WHERE file_uid=old.file_uid;
END;
CREATE TRIGGER saved_pics_update_trigger AFTER UPDATE ON saved_pics
BEGIN
    UPDATE pic_rate_counter SET count=count+1 WHERE rate=new.user_rate;
    UPDATE pic_rate_counter SET count=count-1 WHERE rate=old.user_rate;
END;
CREATE TRIGGER saved_pics_insert_trigger AFTER INSERT ON saved_pics
BEGIN
    INSERT INTO pic_rate_counter(rate,count) VALUES(new.user_rate,1)
    ON CONFLICT(rate) DO UPDATE SET count=count+1;
END;
CREATE TRIGGER saved_pics_delete_trigger AFTER DELETE ON saved_pics
BEGIN
    UPDATE pic_rate_counter SET count=count-1 WHERE rate=old.user_rate;
END;
`
