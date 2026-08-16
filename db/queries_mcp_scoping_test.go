package db_test

import (
	"context"
	"testing"

	"agent-orchestrator/db"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestListMCPServers_CodegraphIsTenantScoped is a regression test for a
// cross-tenant disclosure: ListMCPServers must never return another tenant's
// codegraph servers (whose rows leak project names, repository URLs and
// workspace paths) to a user, even when no company_id is supplied.
func TestListMCPServers_CodegraphIsTenantScoped(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.EnsureSchema(database))
	q := db.New(database)
	ctx := context.Background()

	alice, err := q.CreateUser(ctx, "alice@test.local")
	require.NoError(t, err)
	bob, err := q.CreateUser(ctx, "bob@test.local")
	require.NoError(t, err)

	// A team-less company each, owned by its creator — the tenancy boundary.
	mkCodegraph := func(owner int32, projectName, repo string) db.MCPServer {
		company := db.Company{Name: projectName + " Co", UserID: &owner}
		require.NoError(t, database.Create(&company).Error)
		project := db.Project{CompanyID: company.ID, Name: projectName, RepositoryUrl: repo}
		require.NoError(t, database.Create(&project).Error)
		srv := db.MCPServer{
			Name:        "codegraph-" + projectName,
			DisplayName: projectName,
			Transport:   "builtin",
			ProjectID:   &project.ID,
		}
		require.NoError(t, database.Create(&srv).Error)
		return srv
	}

	aliceSrv := mkCodegraph(alice.ID, "alpha", "git@github.com:alice/alpha.git")
	bobSrv := mkCodegraph(bob.ID, "bravo", "git@github.com:bob/bravo.git")

	// Alice lists with no company filter: she must see her own codegraph server
	// and never Bob's.
	got, err := q.ListMCPServers(ctx, 0, alice.ID)
	require.NoError(t, err)
	ids := map[int32]bool{}
	for _, s := range got {
		ids[s.ID] = true
	}
	require.True(t, ids[aliceSrv.ID], "alice must see her own codegraph server")
	require.False(t, ids[bobSrv.ID], "alice must NOT see bob's codegraph server (cross-tenant leak)")

	// The background cache refresh (userID 0) still sees everything.
	all, err := q.ListMCPServers(ctx, 0, 0)
	require.NoError(t, err)
	require.Len(t, all, 2)
}
