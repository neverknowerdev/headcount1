package db

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupUsersTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(&User{}))
	return database
}

func TestCreateUserMakesOnlyFirstUserAdmin(t *testing.T) {
	database := setupUsersTestDB(t)
	q := New(database)

	first, err := q.CreateUser(context.Background(), "first@example.com")
	require.NoError(t, err)
	second, err := q.CreateUser(context.Background(), "second@example.com")
	require.NoError(t, err)

	require.True(t, first.IsAdmin)
	require.False(t, second.IsAdmin)
}

func TestEnsureFirstUserIsAdminBackfillsLegacyUsers(t *testing.T) {
	database := setupUsersTestDB(t)
	firstCreated := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	secondCreated := firstCreated.Add(time.Minute)
	require.NoError(t, database.Create(&User{Email: "first@example.com", CreatedAt: firstCreated}).Error)
	require.NoError(t, database.Create(&User{Email: "second@example.com", CreatedAt: secondCreated}).Error)

	q := New(database)
	require.NoError(t, q.EnsureFirstUserIsAdmin(context.Background()))

	first, err := q.GetUserByEmail(context.Background(), "first@example.com")
	require.NoError(t, err)
	second, err := q.GetUserByEmail(context.Background(), "second@example.com")
	require.NoError(t, err)
	require.True(t, first.IsAdmin)
	require.False(t, second.IsAdmin)
}
