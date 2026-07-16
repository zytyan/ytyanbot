package q

import (
	"context"
	"database/sql"
	"errors"
	"main/helpers/lrusf"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

var userCache *lrusf.Cache[int64, *User]
var userUpdateLocks [64]sync.Mutex

func (q *Queries) GetUserById(ctx context.Context, id int64) (*User, error) {
	return userCache.Get(id, func() (*User, error) {
		user, err := q.getUserById(ctx, id)
		if err != nil {
			return nil, err
		}
		return &user, nil
	})
}

func (q *Queries) GetOrCreateUserByTg(ctx context.Context, tgUser *gotgbot.User) (*User, error) {
	if tgUser == nil {
		return nil, errors.New("tgUser is nil")
	}
	user, err := q.GetUserById(ctx, tgUser.Id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if user != nil {
		return user, nil
	}
	return q.CreateNewUserByTg(ctx, tgUser)
}
func (q *Queries) CreateNewUserByTg(ctx context.Context, tgUser *gotgbot.User) (*User, error) {
	if tgUser == nil {
		return nil, errors.New("tgUser is nil")
	}
	v, err := userCache.Get(tgUser.Id, func() (*User, error) {
		user, err := q.createNewUser(ctx, createNewUserParams{
			UpdatedAt: UnixTime{time.Now()},
			UserID:    tgUser.Id,
			FirstName: tgUser.FirstName,
			LastName:  sql.NullString{String: tgUser.LastName, Valid: tgUser.LastName != ""},
			Username:  sql.NullString{String: tgUser.Username, Valid: tgUser.Username != ""},
		})
		if err != nil {
			return nil, err
		}
		return &user, nil
	})
	return v, err

}
func (u *User) TryUpdate(q *Queries, tgUser *gotgbot.User) error {
	lock := &userUpdateLocks[uint64(u.UserID)%uint64(len(userUpdateLocks))]
	lock.Lock()
	defer lock.Unlock()
	lastName := sql.NullString{String: tgUser.LastName, Valid: tgUser.LastName != ""}
	username := sql.NullString{String: tgUser.Username, Valid: tgUser.Username != ""}
	if u.FirstName != tgUser.FirstName || u.LastName != lastName || u.Username != username {
		updated, err := q.updateUserBase(context.Background(), updateUserBaseParams{
			UserID:    u.UserID,
			UpdatedAt: UnixTime{Time: time.Now()},
			FirstName: tgUser.FirstName,
			LastName:  lastName,
			Username:  username,
		})
		if err == nil {
			*u = updated
		}
		return err
	}
	return nil
}

func (u *User) Name() string {
	if u == nil {
		return "<unknown>"
	}
	if !u.LastName.Valid || u.LastName.String == "" {
		return u.FirstName
	}
	return u.FirstName + " " + u.LastName.String
}
