package database

import (
	"database/sql"
	"os"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/require"
)

// Run only against the disposable upgrade database, never the live database.
func TestPostgresUpgradePreservesSharedSpace(t *testing.T) {
	dsn := os.Getenv("WEKNORA_UPGRADE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set WEKNORA_UPGRADE_TEST_POSTGRES_DSN to run the upgrade test")
	}
	root := sqliteRepoRoot(t)
	chdirAndRestore(t, root)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	var name string
	require.NoError(t, db.QueryRow("SELECT current_database()").Scan(&name))
	require.Equal(t, "weknora_082_upgrade_test", name)

	m, err := migrate.New("file://migrations/versioned", dsn)
	require.NoError(t, err)
	err = m.Migrate(90)
	require.True(t, err == nil || err == migrate.ErrNoChange, "migration to 90: %v", err)
	_, err = db.Exec(`INSERT INTO organizations (id, name, owner_id, owner_tenant_id)
		VALUES ('upgrade-space', '升级前共享空间', 'upgrade-owner', 10000) ON CONFLICT (id) DO NOTHING;
		INSERT INTO organization_tenant_members
		(id, organization_id, tenant_id, role, representative_user_id)
		VALUES ('upgrade-member', 'upgrade-space', 10000, 'editor', 'upgrade-owner') ON CONFLICT (id) DO NOTHING`)
	require.NoError(t, err)
	_, closeErr := m.Close()
	require.NoError(t, closeErr)
	require.NoError(t, RunMigrationsWithOptions(dsn, MigrationOptions{}))

	var version int
	var dirty bool
	require.NoError(t, db.QueryRow("SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty))
	require.Equal(t, latestVersionedMigration(t, root), version)
	require.False(t, dirty)
	var spaceName, role string
	require.NoError(t, db.QueryRow(`SELECT o.name, m.role FROM organizations o
		JOIN organization_tenant_members m ON m.organization_id = o.id
		WHERE o.id = 'upgrade-space' AND m.id = 'upgrade-member'`).Scan(&spaceName, &role))
	require.Equal(t, "升级前共享空间", spaceName)
	require.Equal(t, "editor", role)
}
