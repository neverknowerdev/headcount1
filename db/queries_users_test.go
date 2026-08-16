package db

import (
	"context"
	"testing"

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
	require.NoError(t, EnsureSchema(database))
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
