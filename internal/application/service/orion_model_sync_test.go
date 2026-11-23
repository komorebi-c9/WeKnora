package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/infrastructure/orion"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- fakes -------------------------------------------------------------

type fakeModelRepo struct {
	rows    map[string]*types.Model
	writes  int
	deletes int
}

func newFakeModelRepo(rows ...*types.Model) *fakeModelRepo {
	repo := &fakeModelRepo{rows: map[string]*types.Model{}}
	for _, row := range rows {
		clone := *row
		repo.rows[row.ID] = &clone
	}
	return repo
}

func (f *fakeModelRepo) Create(_ context.Context, model *types.Model) error {
	f.rows[model.ID] = model
	return nil
}

func (f *fakeModelRepo) GetByID(_ context.Context, tenantID uint64, id string) (*types.Model, error) {
	row, ok := f.rows[id]
	if !ok || row.TenantID != tenantID {
		return nil, nil
	}
	return row, nil
}

func (f *fakeModelRepo) List(
	_ context.Context, tenantID uint64, modelType types.ModelType, source types.ModelSource,
) ([]*types.Model, error) {
	var out []*types.Model
	for _, row := range f.rows {
		if row.TenantID != tenantID {
			continue
		}
		if modelType != "" && row.Type != modelType {
			continue
		}
		if source != "" && row.Source != source {
			continue
		}
		out = append(out, row)
	}
	return out, nil
}

func (f *fakeModelRepo) Update(_ context.Context, model *types.Model) error {
	f.rows[model.ID] = model
	return nil
}

func (f *fakeModelRepo) Delete(_ context.Context, tenantID uint64, id string) error {
	if row, ok := f.rows[id]; ok && row.TenantID == tenantID {
		delete(f.rows, id)
		f.deletes++
	}
	return nil
}

func (f *fakeModelRepo) ClearDefaultByType(
	_ context.Context, tenantID uint, modelType types.ModelType, excludeID string,
) error {
	for _, row := range f.rows {
		if row.TenantID == uint64(tenantID) && row.Type == modelType && row.ID != excludeID {
			row.IsDefault = false
		}
	}
	return nil
}

func (f *fakeModelRepo) ListByManagedBy(
	_ context.Context, managedBy string, tenantID uint64,
) ([]*types.Model, error) {
	var out []*types.Model
	for _, row := range f.rows {
		if row.ManagedBy != managedBy {
			continue
		}
		if tenantID != 0 && row.TenantID != tenantID {
			continue
		}
		out = append(out, row)
	}
	return out, nil
}

func (f *fakeModelRepo) UpsertManaged(
	_ context.Context, model *types.Model, managedBy string,
) error {
	if existing, ok := f.rows[model.ID]; ok && existing.ManagedBy != managedBy {
		return errors.New("refusing to overwrite")
	}
	model.ManagedBy = managedBy
	f.rows[model.ID] = model
	f.writes++
	return nil
}

// fakeRowNeedsUpdate mirrors the real repository's diff semantics closely
// enough for the idempotency test: only fields the sync owns matter.
func fakeRowNeedsUpdate(current, want *types.Model) bool {
	if current == nil || want == nil {
		return true
	}
	return current.Name != want.Name ||
		current.DisplayName != want.DisplayName ||
		current.Type != want.Type ||
		current.Status != want.Status ||
		current.Parameters.BaseURL != want.Parameters.BaseURL ||
		current.Parameters.APIKey != want.Parameters.APIKey ||
		current.Parameters.ContextWindow != want.Parameters.ContextWindow ||
		current.Parameters.EmbeddingParameters.Dimension != want.Parameters.EmbeddingParameters.Dimension ||
		current.Parameters.ExtraConfig["remote_model_name"] != want.Parameters.ExtraConfig["remote_model_name"]
}

// ReconcileManaged mirrors the real transactional reconcile: replaceAll wipes
// the workspace's managed rows first, otherwise stale rows are pruned; the
// desired rows are only written when they actually differ.
func (f *fakeModelRepo) ReconcileManaged(
	_ context.Context, tenantID uint64, managedBy string,
	desired []*types.Model, replaceAll bool,
) (created, updated, deleted int, err error) {
	if replaceAll {
		for id, row := range f.rows {
			if row.TenantID == tenantID && row.ManagedBy == managedBy {
				delete(f.rows, id)
				deleted++
			}
		}
	}
	keep := make(map[string]struct{}, len(desired))
	for _, want := range desired {
		keep[want.ID] = struct{}{}
		existing, ok := f.rows[want.ID]
		if ok && !fakeRowNeedsUpdate(existing, want) {
			continue
		}
		clone := *want
		clone.ManagedBy = managedBy
		if ok && !replaceAll {
			updated++
		} else {
			created++
		}
		f.rows[want.ID] = &clone
		f.writes++
	}
	if !replaceAll {
		for id, row := range f.rows {
			if row.TenantID == tenantID && row.ManagedBy == managedBy {
				if _, keepIt := keep[id]; !keepIt {
					delete(f.rows, id)
					deleted++
				}
			}
		}
	}
	f.deletes += deleted
	return created, updated, deleted, nil
}

type fakeCredentialStore struct {
	key string
	err error
	// configuredCalls counts Configured probes; configuredLimit, when positive,
	// makes every probe past the limit report "not configured". Used to simulate
	// a revoke that lands while a sweep is already running.
	configuredCalls int
	configuredLimit int
}

func (f *fakeCredentialStore) GetAPIKey(context.Context) (string, error) {
	return f.key, f.err
}

func (f *fakeCredentialStore) SaveAPIKey(_ context.Context, key string) error {
	f.key = key
	return nil
}

func (f *fakeCredentialStore) Configured(context.Context) (bool, error) {
	f.configuredCalls++
	if f.err != nil {
		return false, f.err
	}
	if f.configuredLimit > 0 && f.configuredCalls > f.configuredLimit {
		return false, nil
	}
	return f.key != "", nil
}

func (f *fakeCredentialStore) KeyHint(context.Context) (string, error) {
	if f.key == "" {
		return "", nil
	}
	return maskOrionKey(f.key), nil
}

type fakeTenantRepo struct {
	interfaces.TenantRepository
	tenants []*types.Tenant
}

func (f *fakeTenantRepo) ListTenants(context.Context) ([]*types.Tenant, error) {
	return f.tenants, nil
}

type stubOrionCatalog struct {
	entries   []orion.ModelCatalogEntry
	err       error
	dimension int
	probes    int
	keys      []string
}

func (f *stubOrionCatalog) List(_ context.Context, apiKey string) ([]orion.ModelCatalogEntry, error) {
	f.keys = append(f.keys, apiKey)
	if f.err != nil {
		return nil, f.err
	}
	return f.entries, nil
}

func (f *stubOrionCatalog) ProbeEmbeddingDimension(_ context.Context, apiKey, model string) (int, error) {
	f.probes++
	if f.dimension <= 0 {
		return 0, errors.New("probe failed")
	}
	return f.dimension, nil
}

const testTenantID uint64 = 7

func testCfg() *config.OrionModelConfig {
	return &config.OrionModelConfig{
		Enable:              true,
		SyncIntervalSeconds: 300,
		TimeoutSeconds:      10,
		SyncVision:          true,
	}
}

// testOrionBaseURL is the shared Orion gateway origin used by the tests; the
// synced rows point their inference face at its /v1 suffix.
const testOrionBaseURL = "http://orion.local/orion"

func testEntries() []orion.ModelCatalogEntry {
	return []orion.ModelCatalogEntry{
		{Name: "bge-m3-Q8_0", DisplayName: "BGE M3", Type: orion.CatalogTypeEmbedding, Available: true},
		{Name: "bge-reranker-v2-m3-Q8_0", Type: orion.CatalogTypeRerank, Available: true},
		{Name: "local_Qwen3.6-35B-A3B-MTP", Type: orion.CatalogTypeChat, ContextWindow: 131072, Available: true},
		{Name: "offline-model", Type: orion.CatalogTypeChat, Available: false},
	}
}

func newTestSync(
	catalog orion.ModelCatalog, repo *fakeModelRepo, store *fakeCredentialStore, tenants *fakeTenantRepo,
) *OrionModelSync {
	return NewOrionModelSyncWithCatalog(testCfg(), testOrionBaseURL, catalog, repo, store, tenants)
}

func defaultTenants(ids ...uint64) *fakeTenantRepo {
	tenants := &fakeTenantRepo{}
	for _, id := range ids {
		tenants.tenants = append(tenants.tenants, &types.Tenant{ID: id})
	}
	return tenants
}

// ---- tests -------------------------------------------------------------

func TestSyncTenantCreatesTypedRows(t *testing.T) {
	repo := newFakeModelRepo()
	catalog := &stubOrionCatalog{entries: testEntries(), dimension: 1024}
	store := &fakeCredentialStore{key: "sk-global"}
	sync := newTestSync(catalog, repo, store, defaultTenants(testTenantID))

	result, err := sync.SyncTenant(context.Background(), testTenantID)
	require.NoError(t, err)
	assert.Equal(t, 4, result.Total, "chat expands into chat + vision, offline model skipped")
	assert.Equal(t, 4, result.Created)
	assert.Equal(t, 0, result.Deleted)

	rows, err := repo.ListByManagedBy(context.Background(), OrionManagedBy, testTenantID)
	require.NoError(t, err)
	require.Len(t, rows, 4)

	byType := map[types.ModelType]*types.Model{}
	for _, row := range rows {
		byType[row.Type] = row
		assert.Equal(t, testTenantID, row.TenantID)
		assert.Equal(t, types.ModelSourceOrion, row.Source)
		assert.Equal(t, OrionManagedBy, row.ManagedBy)
		assert.False(t, row.IsBuiltin)
		assert.Equal(t, "http://orion.local/orion/v1", row.Parameters.BaseURL)
		assert.Equal(t, "sk-global", row.Parameters.APIKey)
		assert.Equal(t, row.Name, row.Parameters.ExtraConfig["remote_model_name"])
	}

	embedding := byType[types.ModelTypeEmbedding]
	require.NotNil(t, embedding)
	assert.Equal(t, 1024, embedding.Parameters.EmbeddingParameters.Dimension)
	assert.Equal(t, "bge-m3-Q8_0", embedding.Parameters.ExtraConfig["remote_model_name"])

	chat := byType[types.ModelTypeKnowledgeQA]
	require.NotNil(t, chat)
	assert.Equal(t, "local_Qwen3.6-35B-A3B-MTP", chat.Name)
	assert.Equal(t, 131072, chat.Parameters.ContextWindow)

	vision := byType[types.ModelTypeVLLM]
	require.NotNil(t, vision)
	assert.Contains(t, vision.DisplayName, "Vision")
	assert.Equal(t, "local_Qwen3.6-35B-A3B-MTP", vision.Name)

	assert.Contains(t, catalog.keys, "sk-global", "the global credential must be the one used")
}

func TestSyncTenantIsIdempotent(t *testing.T) {
	repo := newFakeModelRepo()
	catalog := &stubOrionCatalog{entries: testEntries(), dimension: 1024}
	store := &fakeCredentialStore{key: "sk-global"}
	sync := newTestSync(catalog, repo, store, defaultTenants(testTenantID))

	_, err := sync.SyncTenant(context.Background(), testTenantID)
	require.NoError(t, err)

	repo.writes = 0
	result, err := sync.SyncTenant(context.Background(), testTenantID)
	require.NoError(t, err)
	assert.Equal(t, 0, result.Created)
	assert.Equal(t, 0, result.Updated)
	assert.Equal(t, 0, repo.writes, "an unchanged catalogue must not rewrite rows")
}

func TestSyncTenantUpdatesAndPrunes(t *testing.T) {
	repo := newFakeModelRepo()
	catalog := &stubOrionCatalog{entries: testEntries(), dimension: 1024}
	store := &fakeCredentialStore{key: "sk-global"}
	sync := newTestSync(catalog, repo, store, defaultTenants(testTenantID))
	_, err := sync.SyncTenant(context.Background(), testTenantID)
	require.NoError(t, err)

	// Orion drops the reranker and trims the chat model's context window.
	catalog.entries = []orion.ModelCatalogEntry{
		{Name: "bge-m3-Q8_0", Type: orion.CatalogTypeEmbedding, Available: true},
		{Name: "local_Qwen3.6-35B-A3B-MTP", Type: orion.CatalogTypeChat, ContextWindow: 32768, Available: true},
	}
	result, err := sync.SyncTenant(context.Background(), testTenantID)
	require.NoError(t, err)
	assert.Equal(t, 3, result.Total)
	assert.Equal(t, 1, result.Deleted, "the reranker row must be soft-deleted")
	assert.GreaterOrEqual(t, result.Updated, 2, "chat context window change propagates to chat + vision rows")

	rows, err := repo.ListByManagedBy(context.Background(), OrionManagedBy, testTenantID)
	require.NoError(t, err)
	for _, row := range rows {
		assert.NotEqual(t, types.ModelTypeRerank, row.Type)
	}
}

func TestSyncTenantDoesNotPruneOnEmptyCatalogue(t *testing.T) {
	existing := &types.Model{
		ID: OrionModelID(testTenantID, "chat", "local_Qwen"), TenantID: testTenantID,
		Name: "local_Qwen", Type: types.ModelTypeKnowledgeQA, Source: types.ModelSourceOrion,
		ManagedBy: OrionManagedBy, Status: types.ModelStatusActive,
	}
	repo := newFakeModelRepo(existing)
	catalog := &stubOrionCatalog{entries: nil}
	store := &fakeCredentialStore{key: "sk-global"}
	sync := newTestSync(catalog, repo, store, defaultTenants(testTenantID))

	_, err := sync.SyncTenant(context.Background(), testTenantID)
	assert.ErrorIs(t, err, ErrOrionCatalogEmpty)
	_, ok := repo.rows[existing.ID]
	assert.True(t, ok, "an empty catalogue must never wipe existing rows")
}

func TestSyncTenantKeepsRowsWhenEveryModelIsUnavailable(t *testing.T) {
	existing := &types.Model{
		ID: OrionModelID(testTenantID, "chat", "local_Qwen"), TenantID: testTenantID,
		Name: "local_Qwen", Type: types.ModelTypeKnowledgeQA, Source: types.ModelSourceOrion,
		ManagedBy: OrionManagedBy, Status: types.ModelStatusActive,
	}
	repo := newFakeModelRepo(existing)
	catalog := &stubOrionCatalog{entries: []orion.ModelCatalogEntry{
		{Name: "local_Qwen", Type: orion.CatalogTypeChat, Available: false},
	}}
	store := &fakeCredentialStore{key: "sk-global"}
	sync := newTestSync(catalog, repo, store, defaultTenants(testTenantID))

	_, err := sync.SyncTenant(context.Background(), testTenantID)
	assert.ErrorIs(t, err, ErrOrionCatalogEmpty)
	_, ok := repo.rows[existing.ID]
	assert.True(t, ok)
}

func TestSyncTenantWithoutKeyKeepsRows(t *testing.T) {
	existing := &types.Model{
		ID: OrionModelID(testTenantID, "chat", "local_Qwen"), TenantID: testTenantID,
		Name: "local_Qwen", Type: types.ModelTypeKnowledgeQA, Source: types.ModelSourceOrion,
		ManagedBy: OrionManagedBy, Status: types.ModelStatusActive,
	}
	repo := newFakeModelRepo(existing)
	catalog := &stubOrionCatalog{entries: testEntries(), dimension: 1024}
	store := &fakeCredentialStore{key: ""}
	sync := newTestSync(catalog, repo, store, defaultTenants(testTenantID))

	_, err := sync.SyncTenant(context.Background(), testTenantID)
	assert.ErrorIs(t, err, ErrOrionKeyMissing)
	assert.Empty(t, catalog.keys, "without the global credential Orion must not be called")
	_, ok := repo.rows[existing.ID]
	assert.True(t, ok, "a missing credential must never wipe existing rows")
}

func TestSyncTenantRotatedKeyReplacesAllRows(t *testing.T) {
	repo := newFakeModelRepo()
	catalog := &stubOrionCatalog{entries: testEntries(), dimension: 1024}
	store := &fakeCredentialStore{key: "sk-old"}
	sync := newTestSync(catalog, repo, store, defaultTenants(testTenantID))
	_, err := sync.SyncTenant(context.Background(), testTenantID)
	require.NoError(t, err)

	// The platform admin saves a new licence: every row still carrying the
	// old credential must be replaced, not patched.
	store.key = "sk-new"
	result, err := sync.SyncTenant(context.Background(), testTenantID)
	require.NoError(t, err)
	assert.Equal(t, 4, result.Deleted, "all rows keyed to the old credential must be removed")
	assert.Equal(t, 4, result.Created, "the new catalogue must be inserted fresh")

	rows, err := repo.ListByManagedBy(context.Background(), OrionManagedBy, testTenantID)
	require.NoError(t, err)
	require.Len(t, rows, 4)
	for _, row := range rows {
		assert.Equal(t, "sk-new", row.Parameters.APIKey,
			"no row may keep the rotated-out credential")
	}
}

func TestSyncAllReusesCatalogueAcrossTenants(t *testing.T) {
	repo := newFakeModelRepo()
	// The embedding model is deliberately unknown to WeKnora's built-in
	// catalogue so the sweep must fall back to a live dimension probe.
	entries := testEntries()
	entries[0].Name = "totally-unknown-embedder"
	catalog := &stubOrionCatalog{entries: entries, dimension: 1024}
	store := &fakeCredentialStore{key: "sk-global"}
	tenants := defaultTenants(1, 2, 3)
	sync := newTestSync(catalog, repo, store, tenants)

	sync.SyncAll(context.Background())

	assert.Len(t, catalog.keys, 1, "one sweep must pull the catalogue exactly once")
	for _, tenantID := range []uint64{1, 2, 3} {
		rows, err := repo.ListByManagedBy(context.Background(), OrionManagedBy, tenantID)
		require.NoError(t, err)
		require.Len(t, rows, 4, "every workspace must end up with the same models")
		for _, row := range rows {
			assert.Equal(t, tenantID, row.TenantID)
			assert.Equal(t, "sk-global", row.Parameters.APIKey)
		}
	}
	assert.Equal(t, 1, catalog.probes,
		"the embedding dimension is probed once and memoised across tenants")
}

func TestSyncAllSkipsWhenKeyMissing(t *testing.T) {
	repo := newFakeModelRepo()
	catalog := &stubOrionCatalog{entries: testEntries(), dimension: 1024}
	store := &fakeCredentialStore{key: ""}
	sync := newTestSync(catalog, repo, store, defaultTenants(1, 2))

	sync.SyncAll(context.Background())
	assert.Empty(t, catalog.keys, "without the global credential Orion must not be called")
	assert.Empty(t, repo.rows)
}

func TestSyncTenantSetsOneDefaultPerType(t *testing.T) {
	repo := newFakeModelRepo()
	catalog := &stubOrionCatalog{entries: testEntries(), dimension: 1024}
	store := &fakeCredentialStore{key: "sk-global"}
	sync := newTestSync(catalog, repo, store, defaultTenants(testTenantID))
	_, err := sync.SyncTenant(context.Background(), testTenantID)
	require.NoError(t, err)

	counts := map[types.ModelType]int{}
	for _, row := range repo.rows {
		if row.IsDefault {
			counts[row.Type]++
		}
	}
	assert.Equal(t, 1, counts[types.ModelTypeKnowledgeQA])
	assert.Equal(t, 1, counts[types.ModelTypeVLLM])
	assert.Equal(t, 1, counts[types.ModelTypeEmbedding])
	assert.Equal(t, 1, counts[types.ModelTypeRerank])
}

func TestSyncTenantSkipsEmbeddingWithoutDimension(t *testing.T) {
	repo := newFakeModelRepo()
	catalog := &stubOrionCatalog{
		entries: []orion.ModelCatalogEntry{
			{Name: "totally-unknown-embedder", Type: orion.CatalogTypeEmbedding, Available: true},
			{Name: "local_Qwen", Type: orion.CatalogTypeChat, Available: true},
		},
		dimension: 0,
	}
	store := &fakeCredentialStore{key: "sk-global"}
	sync := newTestSync(catalog, repo, store, defaultTenants(testTenantID))

	_, err := sync.SyncTenant(context.Background(), testTenantID)
	require.NoError(t, err)
	for _, row := range repo.rows {
		assert.NotEqual(t, types.ModelTypeEmbedding, row.Type,
			"an embedding model whose width cannot be resolved must not be published")
	}
}

func TestOrionModelIDIsBoundedAndURLSafe(t *testing.T) {
	long := "some/very long model name with spaces and 中文 " + strings.Repeat("x", 120)
	id := OrionModelID(123456789, "chat", long)
	assert.LessOrEqual(t, len(id), types.ModelIDMaxLen)
	assert.NotContains(t, id, "/")
	assert.NotContains(t, id, " ")
	assert.NotContains(t, id, "中文")

	assert.Equal(t, id, OrionModelID(123456789, "chat", long), "ids must be stable")
	assert.NotEqual(t,
		OrionModelID(testTenantID, "chat", "same"),
		OrionModelID(testTenantID, "vlm", "same"),
		"chat and vision rows of the same model need distinct ids",
	)
}

// A revoke clears the credential, and from that moment a running sweep must stop
// instead of repopulating the rows the purge is about to remove.
func TestSyncAllAbortsWhenCredentialIsRevokedMidSweep(t *testing.T) {
	repo := newFakeModelRepo()
	catalog := &stubOrionCatalog{entries: testEntries(), dimension: 1024}
	// The first probe (workspace 1) still sees the credential; every later one
	// sees it gone, which is what a revoke landing mid-sweep looks like.
	store := &fakeCredentialStore{key: "sk-global", configuredLimit: 1}
	sync := newTestSync(catalog, repo, store, defaultTenants(1, 2, 3))

	sync.SyncAll(context.Background())

	synced, err := repo.ListByManagedBy(context.Background(), OrionManagedBy, 1)
	require.NoError(t, err)
	assert.Len(t, synced, 4)
	for _, tenantID := range []uint64{2, 3} {
		rows, err := repo.ListByManagedBy(context.Background(), OrionManagedBy, tenantID)
		require.NoError(t, err)
		assert.Empty(t, rows, "the sweep must abort once the credential is gone")
	}
}

func TestPurgeAllClearsEveryWorkspaceAndKeepsForeignRows(t *testing.T) {
	manual := &types.Model{
		ID: "manual-embed", TenantID: 1, Name: "manual", Type: types.ModelTypeEmbedding,
		Source: types.ModelSourceRemote, Status: types.ModelStatusActive,
	}
	repo := newFakeModelRepo(manual)
	catalog := &stubOrionCatalog{entries: testEntries(), dimension: 1024}
	store := &fakeCredentialStore{key: "sk-global"}
	sync := newTestSync(catalog, repo, store, defaultTenants(1, 2, 3))
	sync.SyncAll(context.Background())
	require.Len(t, repo.rows, 13, "12 synced rows + 1 hand-created row")

	// Revoke: the credential is cleared first, then the models go.
	store.key = ""
	deleted, err := sync.PurgeAll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 12, deleted)

	rows, err := repo.ListByManagedBy(context.Background(), OrionManagedBy, 0)
	require.NoError(t, err)
	assert.Empty(t, rows, "no workspace may keep a row synced from the revoked key")
	_, kept := repo.rows[manual.ID]
	assert.True(t, kept, "hand-created models are never touched")

	status := sync.Status(context.Background(), 1)
	assert.False(t, status.KeyConfigured)
	assert.Equal(t, 0, status.ModelCount)
	assert.True(t, status.LastSyncAt.IsZero(), "the purge forgets the previous sync")

	// A second revoke (the UI retrying after a partial failure) is a no-op.
	deleted, err = sync.PurgeAll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, deleted)
}

func TestSyncTenantDisabled(t *testing.T) {
	cfg := testCfg()
	cfg.Enable = false
	sync := NewOrionModelSyncWithCatalog(
		cfg, testOrionBaseURL, &stubOrionCatalog{}, newFakeModelRepo(), &fakeCredentialStore{}, defaultTenants(testTenantID))
	_, err := sync.SyncTenant(context.Background(), testTenantID)
	assert.ErrorIs(t, err, ErrOrionModelDisabled)
}
