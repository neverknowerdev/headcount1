package db

import (
	"context"

	"agent-orchestrator/pkg/secrets"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// gormUserKeyStorage backs pkg/secrets' per-user key material with the
// user_keys table. Installed via secrets.SetUserKeyStorage in main.go (and in
// tests that exercise "enc:u1:" sealing).
type gormUserKeyStorage struct{ db *gorm.DB }

func NewUserKeyStorage(database *gorm.DB) secrets.UserKeyStorage {
	return gormUserKeyStorage{db: database}
}

func (s gormUserKeyStorage) GetUserKey(userID int32) (secrets.UserKeyRecord, error) {
	var k UserKey
	if err := s.db.First(&k, "user_id = ?", userID).Error; err != nil {
		return secrets.UserKeyRecord{}, err
	}
	return secrets.UserKeyRecord{
		WrappedDEKServer:   k.WrappedDEKServer,
		WrappedDEKPassword: k.WrappedDEKPassword,
		PwSalt:             k.PwSalt,
		PwParams:           k.PwParams,
	}, nil
}

func (s gormUserKeyStorage) ListUserKeys() (map[int32]secrets.UserKeyRecord, error) {
	var keys []UserKey
	if err := s.db.Find(&keys).Error; err != nil {
		return nil, err
	}
	recs := make(map[int32]secrets.UserKeyRecord, len(keys))
	for _, k := range keys {
		recs[k.UserID] = secrets.UserKeyRecord{
			WrappedDEKServer:   k.WrappedDEKServer,
			WrappedDEKPassword: k.WrappedDEKPassword,
			PwSalt:             k.PwSalt,
			PwParams:           k.PwParams,
		}
	}
	return recs, nil
}

func (s gormUserKeyStorage) SaveUserKey(userID int32, rec secrets.UserKeyRecord) error {
	k := UserKey{
		UserID:             userID,
		WrappedDEKServer:   rec.WrappedDEKServer,
		WrappedDEKPassword: rec.WrappedDEKPassword,
		PwSalt:             rec.PwSalt,
		PwParams:           rec.PwParams,
	}
	return s.db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&k).Error
}

// DeleteUserKey removes a user's key material, crypto-shredding every
// "enc:u1:" secret they own (the ciphertext rows become undecryptable).
// User deletion cascades here automatically via the FK.
func (q *Queries) DeleteUserKey(ctx context.Context, userID int32) error {
	return q.db.WithContext(ctx).Delete(&UserKey{}, "user_id = ?", userID).Error
}
