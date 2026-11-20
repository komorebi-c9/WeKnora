package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newManagedModelDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&types.Model{}))
	return db
}

func managedOrionRow(tenantID uint64, displayName string) *types.Model {
	return &types.Model{
		ID:          "orion-1-chat-local_Qwen",
		TenantID:    tenantID,
		Name:        "local_Qwen",
		DisplayName: displayName,
		Type:        types.ModelTypeKnowledgeQA,
		Source:      types.ModelSourceOrion,
		Description: "Synced from Orion",
		Parameters: types.ModelParameters{
			BaseURL:  "http://orion.local/orion/v1",
			APIKey:   "sk-test",
			Provider: "orion",
			ExtraConfig: map[string]string{
				"remote_model_name": "local_Qwen",
			},
		},
		ManagedBy: "orion",
		Status:    types.ModelStatusActive,
	}
}

func TestUpsertManagedCreatesAndUpdates(t *testing.T) {
	db := newManagedModelDB(t)
	repo := NewModelRepository(db).(*modelRepository)
	ctx := context.Background()

	require.NoError(t, repo.UpsertManaged(ctx, managedOrionRow(7, "Qwen"), "orion"))

	rows, err := repo.ListByManagedBy(ctx, "orion", 7)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "Qwen", rows[0].DisplayName)
	createdAt := rows[0].CreatedAt

	// Second sync: same id, changed display name and parameters.
	updated := managedOrionRow(7, "Qwen 2")
	updated.Parameters.ContextWindow = 32768
	time.Sleep(2 * time.Millisecond)
	require.NoError(t, repo.UpsertManaged(ctx, updated, "orion"))

	rows, err = repo.ListByManagedBy(ctx, "orion", 7)
	require.NoError(t, err)
	require.Len(t, rows, 1, "the primary key must keep the row unique")
	assert.Equal(t, "Qwen 2", rows[0].DisplayName)
	assert.Equal(t, 32768, rows[0].Parameters.ContextWindow)
	assert.WithinDuration(t, createdAt, rows[0].CreatedAt, time.Second,
		"created_at belongs to the first sync and must not be rewritten")

	// Another workspace uses the same model but a different id.
	require.NoError(t, repo.UpsertManaged(ctx, managedOrionRow(8, "Qwen"), "orion"))
	other, err := repo.ListByManagedBy(ctx, "orion", 8)
	require.NoError(t, err)
	require.Len(t, other, 1)
	assert.Equal(t, uint64(8), other[0].TenantID)
}

func TestUpsertManagedRevivesSoftDeletedRow(t *testing.T) {
	db := newManagedModelDB(t)
	repo := NewModelRepository(db).(*modelRepository)
	ctx := context.Background()

	require.NoError(t, repo.UpsertManaged(ctx, managedOrionRow(7, "Qwen"), "orion"))
	require.NoError(t, repo.Delete(ctx, 7, "orion-1-chat-local_Qwen"))

	rows, err := repo.ListByManagedBy(ctx, "orion", 7)
	require.NoError(t, err)
	assert.Empty(t, rows, "a removed model disappears from the live catalogue")

	// Orion lists it again: the same id must come back instead of a duplicate.
	require.NoError(t, repo.UpsertManaged(ctx, managedOrionRow(7, "Qwen again"), "orion"))

	rows, err = repo.ListByManagedBy(ctx, "orion", 7)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "Qwen again", rows[0].DisplayName)
	assert.False(t, rows[0].DeletedAt.Valid, "deleted_at must be cleared on revival")
}

func TestUpsertManagedRefusesForeignRows(t *testing.T) {
	db := newManagedModelDB(t)
	repo := NewModelRepository(db).(*modelRepository)
	ctx := context.Background()

	manual := managedOrionRow(7, "hand made")
	manual.ManagedBy = ""
	manual.Source = types.ModelSourceRemote
	require.NoError(t, db.Create(manual).Error)

	err := repo.UpsertManaged(ctx, managedOrionRow(7, "orion copy"), "orion")
	require.Error(t, err, "a manual row must never be overwritten by the sync")
	assert.Contains(t, err.Error(), "refusing to overwrite")

	var stored types.Model
	require.NoError(t, db.Where("id = ?", manual.ID).First(&stored).Error)
	assert.Equal(t, "", stored.ManagedBy)
	assert.Equal(t, "hand made", stored.DisplayName)
}

func TestListByManagedByScopesTenant(t *testing.T) {
	db := newManagedModelDB(t)
	repo := NewModelRepository(db).(*modelRepository)
	ctx := context.Background()

	require.NoError(t, repo.UpsertManaged(ctx, managedOrionRow(7, "seven"), "orion"))
	row := managedOrionRow(8, "eight")
	row.ID = "orion-8-chat-local_Qwen"
	require.NoError(t, repo.UpsertManaged(ctx, row, "orion"))

	all, err := repo.ListByManagedBy(ctx, "orion", 0)
	require.NoError(t, err)
	assert.Len(t, all, 2)

	scoped, err := repo.ListByManagedBy(ctx, "orion", 8)
	require.NoError(t, err)
	require.Len(t, scoped, 1)
	assert.Equal(t, "eight", scoped[0].DisplayName)
}

func TestReconcileManagedReplaceAllSwapsRowsAtomically(t *testing.T) {
	db := newManagedModelDB(t)
	repo := NewModelRepository(db).(*modelRepository)
	ctx := context.Background()

	// Two rows synced under the old credential.
	require.NoError(t, repo.UpsertManaged(ctx, managedOrionRow(7, "old chat"), "orion"))
	oldRerank := managedOrionRow(7, "old rerank")
	oldRerank.ID = "orion-7-rer-bge"
	oldRerank.Type = types.ModelTypeRerank
	oldRerank.Name = "bge-reranker"
	require.NoError(t, repo.UpsertManaged(ctx, oldRerank, "orion"))

	// Key rotation: the desired set carries the new credential and only one
	// of the previous models. The swap must be delete-then-insert.
	desired := []*types.Model{managedOrionRow(7, "new chat")}
	created, updated, deleted, err := repo.ReconcileManaged(ctx, 7, "orion", desired, true)
	require.NoError(t, err)
	assert.Equal(t, 1, created)
	assert.Equal(t, 0, updated)
	assert.Equal(t, 2, deleted)

	rows, err := repo.ListByManagedBy(ctx, "orion", 7)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "new chat", rows[0].DisplayName)
	assert.Equal(t, "sk-test", rows[0].Parameters.APIKey)
}

func TestReconcileManagedDiffPrunesStaleRows(t *testing.T) {
	db := newManagedModelDB(t)
	repo := NewModelRepository(db).(*modelRepository)
	ctx := context.Background()

	require.NoError(t, repo.UpsertManaged(ctx, managedOrionRow(7, "old chat"), "orion"))
	stale := managedOrionRow(7, "stale rerank")
	stale.ID = "orion-7-rer-bge"
	stale.Type = types.ModelTypeRerank
	stale.Name = "bge-reranker"
	require.NoError(t, repo.UpsertManaged(ctx, stale, "orion"))

	// Same credential, catalogue changed: prune the stale row, keep + update
	// the rest. A manual row owned by nobody must survive untouched.
	manual := managedOrionRow(7, "hand made")
	manual.ID = "manual-chat-qwen"
	manual.ManagedBy = ""
	manual.Source = types.ModelSourceRemote
	require.NoError(t, db.Create(manual).Error)

	updatedRow := managedOrionRow(7, "updated chat")
	updatedRow.Parameters.ContextWindow = 32768
	desired := []*types.Model{updatedRow}
	created, updated, deleted, err := repo.ReconcileManaged(ctx, 7, "orion", desired, false)
	require.NoError(t, err)
	assert.Equal(t, 0, created)
	assert.Equal(t, 1, updated)
	assert.Equal(t, 1, deleted)

	rows, err := repo.ListByManagedBy(ctx, "orion", 7)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, 32768, rows[0].Parameters.ContextWindow)

	var manualStored types.Model
	require.NoError(t, db.Where("id = ?", manual.ID).First(&manualStored).Error)
	assert.Equal(t, "hand made", manualStored.DisplayName,
		"rows not owned by the managed subsystem must never be touched")
}

func TestReconcileManagedRollsBackOnForeignCollision(t *testing.T) {
	db := newManagedModelDB(t)
	repo := NewModelRepository(db).(*modelRepository)
	ctx := context.Background()

	// A manual row occupies the primary key the sync wants to write.
	manual := managedOrionRow(7, "hand made")
	manual.ManagedBy = ""
	manual.Source = types.ModelSourceRemote
	require.NoError(t, db.Create(manual).Error)

	desired := []*types.Model{managedOrionRow(7, "orion copy")}
	_, _, _, err := repo.ReconcileManaged(ctx, 7, "orion", desired, true)
	require.Error(t, err, "a manual row must never be overwritten by the sync")

	after, err := repo.ListByManagedBy(ctx, "orion", 7)
	require.NoError(t, err)
	assert.Empty(t, after, "the failed transaction must leave no partial state")

	var manualStored types.Model
	require.NoError(t, db.Where("id = ?", manual.ID).First(&manualStored).Error)
	assert.Equal(t, "hand made", manualStored.DisplayName,
		"the rollback must restore the exact pre-sync state")
}
