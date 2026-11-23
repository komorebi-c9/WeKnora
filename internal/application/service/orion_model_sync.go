package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/infrastructure/orion"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/providers"
	"github.com/Tencent/WeKnora/internal/models/runtime"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// OrionManagedBy is the models.managed_by value that marks rows owned by the
// Orion synchroniser. Every write path in Orion mode is restricted to rows with
// this marker, and the startup sweep never touches anything else.
const OrionManagedBy = "orion"

var (
	// ErrOrionModelDisabled means the Orion model switch is off.
	ErrOrionModelDisabled = errors.New("orion model mode is disabled")
	// ErrOrionKeyMissing means the platform-wide Orion credential has not been
	// configured yet (system_settings "orion.api_key", set by a system admin).
	ErrOrionKeyMissing = errors.New(
		"orion api key is not configured; ask the platform administrator to configure it")
	// ErrOrionSyncInProgress means another sync for the same workspace is running.
	ErrOrionSyncInProgress = errors.New("orion model sync is already running")
	// ErrOrionCatalogEmpty means Orion reported no models. The synchroniser
	// treats this as an anomaly and never prunes on it: a transient upstream
	// problem must not wipe every model the workspace is using.
	ErrOrionCatalogEmpty = errors.New("orion returned an empty model catalogue")
)

// OrionSyncResult reports one workspace reconciliation.
type OrionSyncResult struct {
	Total      int
	Created    int
	Updated    int
	Deleted    int
	Skipped    int
	StartedAt  time.Time
	FinishedAt time.Time
}

// OrionSyncStatus is the read model behind the model page's status bar.
type OrionSyncStatus struct {
	Enabled             bool
	KeyConfigured       bool
	LastSyncAt          time.Time
	ModelCount          int
	LastError           string
	SyncIntervalSeconds int
}

// OrionCredentialSource is the read view of the platform-wide credential the
// synchroniser depends on. Keeping it an interface lets tests inject a stub
// while production wires *OrionCredentialStore.
type OrionCredentialSource interface {
	// GetAPIKey returns the decrypted global key; "" means not configured.
	GetAPIKey(ctx context.Context) (string, error)
	// Configured reports whether a non-empty global key exists.
	Configured(ctx context.Context) (bool, error)
}

type orionTenantState struct {
	lastSyncAt time.Time
	lastError  string
}

// OrionModelSync pulls the platform-wide catalogue from Orion and keeps every
// workspace's rows in step with it. All tenants share one credential (the
// global Orion License SK from OrionCredentialStore), so every user sees the
// same models.
//
// It writes through the repository directly, deliberately bypassing the
// read-only decorator that fronts the HTTP API.
type OrionModelSync struct {
	cfg             *config.OrionModelConfig
	baseURL         string
	catalog         orion.ModelCatalog
	modelRepo       interfaces.ModelRepository
	credentialStore OrionCredentialSource
	tenantRepo      interfaces.TenantRepository

	mu      sync.Mutex
	running map[uint64]struct{}
	states  map[uint64]orionTenantState
}

// OrionGatewayPath is the path prefix under which the Orion service exposes
// everything: identity (/orion/v1/iam/me), the model catalogue
// (/orion/v1/models) and inference (/orion/v1/...). ORION_BASE_URL carries
// only the origin, e.g. http://192.168.3.139:9298, so the model side appends
// this prefix to rebuild the gateway root the old ORION_MODEL_BASE_URL used
// to point at (e.g. http://192.168.3.139:9298/orion).
const OrionGatewayPath = "/orion"

// NewOrionModelSync builds the production synchroniser. When the switch is off
// the catalog client is nil and every entry point reports ErrOrionModelDisabled.
// The gateway root is the one shared origin configured via orion_auth.base_url,
// plus OrionGatewayPath.
func NewOrionModelSync(
	cfg *config.Config,
	modelRepo interfaces.ModelRepository,
	credentialStore *OrionCredentialStore,
	tenantRepo interfaces.TenantRepository,
) *OrionModelSync {
	modelCfg := &config.OrionModelConfig{}
	baseURL := ""
	if cfg != nil {
		if cfg.OrionModel != nil {
			modelCfg = cfg.OrionModel
		}
		baseURL = strings.TrimRight(cfg.OrionBaseURL(), "/") + OrionGatewayPath
	}
	var catalog orion.ModelCatalog
	if modelCfg.IsEnabled() {
		catalog = orion.NewModelCatalog(baseURL, modelCfg.Timeout())
	}
	return NewOrionModelSyncWithCatalog(modelCfg, baseURL, catalog, modelRepo, credentialStore, tenantRepo)
}

// NewOrionModelSyncWithCatalog injects the catalog client (tests, or a future
// transport). baseURL is the Orion gateway root, i.e. the shared origin plus
// OrionGatewayPath — the catalogue and inference both live under
// {baseURL}/v1 (GET /models, POST /embeddings, ...).
func NewOrionModelSyncWithCatalog(
	cfg *config.OrionModelConfig,
	baseURL string,
	catalog orion.ModelCatalog,
	modelRepo interfaces.ModelRepository,
	credentialStore OrionCredentialSource,
	tenantRepo interfaces.TenantRepository,
) *OrionModelSync {
	if cfg == nil {
		cfg = &config.OrionModelConfig{}
	}
	return &OrionModelSync{
		cfg:             cfg,
		baseURL:         baseURL,
		catalog:         catalog,
		modelRepo:       modelRepo,
		credentialStore: credentialStore,
		tenantRepo:      tenantRepo,
		running:         make(map[uint64]struct{}),
		states:          make(map[uint64]orionTenantState),
	}
}

// Enabled reports whether Orion owns the catalogue.
func (s *OrionModelSync) Enabled() bool {
	return s != nil && s.cfg.IsEnabled()
}

// SyncAllBackground runs SyncAll in a detached goroutine with its own timeout,
// for fire-and-forget callers such as the api-key save endpoint: saving the
// credential returns immediately, the resulting reconciliation proceeds in the
// background and failures only show up in the per-workspace status.
func (s *OrionModelSync) SyncAllBackground() {
	if s == nil || !s.Enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), orionSyncRunTimeout)
	go func() {
		defer cancel()
		s.SyncAll(ctx)
	}()
}

// SyncTenantBackground runs SyncTenant in a detached goroutine with its own
// timeout, for fire-and-forget callers such as Orion account provisioning:
// the workspace is created during login, and the login must not block on a
// catalogue round-trip. Failures only show up in the per-workspace status
// and the log; the periodic sweep reconciles anything left behind.
func (s *OrionModelSync) SyncTenantBackground(tenantID uint64) {
	if s == nil || !s.Enabled() || tenantID == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), orionSyncRunTimeout)
	go func() {
		defer cancel()
		// A workspace provisioned before the global credential exists has
		// nothing to reconcile. That is not a sync failure, so it is skipped
		// with an info log and left off the per-workspace error status; the
		// periodic sweep (and the next save of the credential) fills the gap.
		if s.credentialStore != nil {
			if configured, err := s.credentialStore.Configured(ctx); err == nil && !configured {
				logger.Infof(ctx,
					"[orion-model] global orion api key not configured; skipping model sync for workspace %d",
					tenantID)
				return
			}
		}
		if _, err := s.SyncTenant(ctx, tenantID); err != nil {
			if errors.Is(err, ErrOrionKeyMissing) {
				logger.Infof(ctx,
					"[orion-model] global orion api key not configured; skipping model sync for workspace %d",
					tenantID)
				return
			}
			logger.Warnf(ctx,
				"[orion-model] background sync for workspace %d failed: %v", tenantID, err)
		}
	}()
}

// orionPurgeWaitTimeout bounds how long a revoke waits for an in-flight
// workspace sync to release its guard. A sync that started before the credential
// was cleared may still write rows; waiting it out is what makes the purge final
// instead of racing it.
const orionPurgeWaitTimeout = 10 * time.Second

// PurgeAll removes every Orion-managed model row in every workspace and reports
// how many rows were removed.
//
// The caller must clear the global credential first: with no key, the periodic
// sweep and the background refreshes become no-ops, so a purge cannot be undone
// by a sync that starts afterwards. This method only has to wait out a sync that
// was already in flight.
//
// Rows are soft-deleted through ReconcileManaged with an empty desired set —
// the same primitive a credential rotation uses — so a later re-authorisation
// revives them with their original ids and workspace bindings.
func (s *OrionModelSync) PurgeAll(ctx context.Context) (int, error) {
	if s == nil || s.modelRepo == nil || s.tenantRepo == nil {
		return 0, nil
	}
	tenants, err := s.tenantRepo.ListTenants(ctx)
	if err != nil {
		return 0, fmt.Errorf("list workspaces: %w", err)
	}

	deleted := 0
	var firstErr error
	for _, tenant := range tenants {
		if tenant == nil || tenant.ID == 0 {
			continue
		}
		if !s.acquire(ctx, tenant.ID, orionPurgeWaitTimeout) {
			if firstErr == nil {
				firstErr = fmt.Errorf("%w: workspace %d", ErrOrionSyncInProgress, tenant.ID)
			}
			logger.Warnf(ctx,
				"[orion-model] workspace %d is still syncing; its orion models were left in place",
				tenant.ID)
			continue
		}
		_, _, removed, purgeErr := s.modelRepo.ReconcileManaged(
			ctx, tenant.ID, OrionManagedBy, nil, true)
		s.end(tenant.ID)
		if purgeErr != nil {
			if firstErr == nil {
				firstErr = purgeErr
			}
			logger.Warnf(ctx, "[orion-model] purging workspace %d failed: %v", tenant.ID, purgeErr)
			continue
		}
		deleted += removed
		s.clearState(tenant.ID)
	}
	logger.Infof(ctx, "[orion-model] purged %d orion-managed model rows", deleted)
	return deleted, firstErr
}

// acquire takes the per-workspace sync guard, waiting up to timeout for an
// in-flight sync to release it.
func (s *OrionModelSync) acquire(ctx context.Context, tenantID uint64, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if s.begin(tenantID) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// clearState forgets the workspace's last sync so the status bar stops
// advertising a reconciliation that no longer has any models behind it.
func (s *OrionModelSync) clearState(tenantID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.states, tenantID)
}

// SyncAll reconciles every workspace against the shared global credential.
// The catalogue is pulled once (all tenants see the same models) and each
// workspace is then reconciled in its own transaction. One workspace failing
// never stops the rest; the caller only gets a summary in the log.
func (s *OrionModelSync) SyncAll(ctx context.Context) {
	if s == nil || !s.cfg.IsEnabled() || s.catalog == nil {
		return
	}
	apiKey, err := s.credentialStore.GetAPIKey(ctx)
	if err != nil {
		logger.Warnf(ctx, "[orion-model] reading the global credential failed: %v", err)
		return
	}
	if apiKey == "" {
		logger.Infof(ctx, "[orion-model] global orion api key not configured; skipping sync")
		return
	}

	entries, listErr := s.catalog.List(ctx, apiKey)
	if listErr != nil {
		logger.Warnf(ctx, "[orion-model] listing the orion catalogue failed: %v", listErr)
		return
	}
	if len(entries) == 0 {
		logger.Warnf(ctx, "[orion-model] orion returned an empty catalogue; nothing to sync")
		return
	}

	tenants, err := s.tenantRepo.ListTenants(ctx)
	if err != nil {
		logger.Warnf(ctx, "[orion-model] listing workspaces failed: %v", err)
		return
	}

	// Embedding dimension probes only depend on the model name and the shared
	// key, so one cache serves the whole sweep.
	probeCache := make(map[string]int)
	for _, tenant := range tenants {
		if tenant == nil || tenant.ID == 0 {
			continue
		}
		// The credential can be revoked while a sweep is still walking the
		// workspaces. Stop at the next boundary instead of repopulating rows the
		// revoke just purged.
		if s.credentialStore != nil {
			if configured, err := s.credentialStore.Configured(ctx); err == nil && !configured {
				logger.Infof(ctx,
					"[orion-model] global orion api key was cleared; aborting the sweep")
				return
			}
		}
		if _, err := s.syncTenant(ctx, tenant.ID, apiKey, entries, probeCache); err != nil {
			s.recordFailure(tenant.ID, err)
			logger.Warnf(ctx, "[orion-model] workspace %d sync failed: %v", tenant.ID, err)
		}
	}
}

// SyncTenant reconciles one workspace using the global credential. It is the
// manual-refresh path: it pulls the catalogue itself instead of reusing a
// sweep's copy.
func (s *OrionModelSync) SyncTenant(ctx context.Context, tenantID uint64) (OrionSyncResult, error) {
	result := OrionSyncResult{StartedAt: time.Now()}
	if s == nil || !s.cfg.IsEnabled() || s.catalog == nil {
		return result, ErrOrionModelDisabled
	}
	if tenantID == 0 {
		return result, errors.New("tenant id is required")
	}

	apiKey, err := s.credentialStore.GetAPIKey(ctx)
	if err != nil {
		s.recordFailure(tenantID, err)
		result.FinishedAt = time.Now()
		return result, err
	}
	if apiKey == "" {
		// No credential: nothing can be verified, so nothing is pruned either.
		s.recordFailure(tenantID, ErrOrionKeyMissing)
		result.FinishedAt = time.Now()
		return result, ErrOrionKeyMissing
	}

	entries, listErr := s.catalog.List(ctx, apiKey)
	if listErr != nil {
		s.recordFailure(tenantID, listErr)
		result.FinishedAt = time.Now()
		return result, listErr
	}
	if len(entries) == 0 {
		s.recordFailure(tenantID, ErrOrionCatalogEmpty)
		result.FinishedAt = time.Now()
		return result, ErrOrionCatalogEmpty
	}

	return s.syncTenant(ctx, tenantID, apiKey, entries, nil)
}

// syncTenant runs the guarded per-workspace reconciliation against an already
// fetched catalogue.
func (s *OrionModelSync) syncTenant(
	ctx context.Context, tenantID uint64, apiKey string,
	entries []orion.ModelCatalogEntry, probeCache map[string]int,
) (OrionSyncResult, error) {
	result := OrionSyncResult{StartedAt: time.Now()}
	if !s.begin(tenantID) {
		return result, ErrOrionSyncInProgress
	}
	defer s.end(tenantID)

	total, created, updated, deleted, err := s.reconcile(ctx, tenantID, apiKey, entries, probeCache)
	if err != nil {
		s.recordFailure(tenantID, err)
		result.FinishedAt = time.Now()
		return result, err
	}

	result.Total = total
	result.Created = created
	result.Updated = updated
	result.Deleted = deleted
	result.FinishedAt = time.Now()
	s.recordSuccess(tenantID)
	logger.Infof(ctx,
		"[orion-model] workspace %d synced: total=%d created=%d updated=%d deleted=%d elapsed=%s",
		tenantID, result.Total, result.Created, result.Updated, result.Deleted,
		result.FinishedAt.Sub(result.StartedAt))
	return result, nil
}

// reconcile swaps the workspace's Orion rows for the desired set inside a
// single repository transaction. When any existing row carries a credential
// other than the current global key, the swap is a full replace (delete-then-
// insert): stale rows must never survive a key rotation, and the transaction
// guarantees readers never observe the intermediate "no models" state.
func (s *OrionModelSync) reconcile(
	ctx context.Context, tenantID uint64, apiKey string,
	entries []orion.ModelCatalogEntry, probeCache map[string]int,
) (total, created, updated, deleted int, err error) {
	desired := s.desiredModels(ctx, tenantID, apiKey, entries, probeCache)
	if len(desired) == 0 {
		// Every listed model is unavailable right now. Keep the previous rows:
		// availability is transient, existence is not.
		return 0, 0, 0, 0, ErrOrionCatalogEmpty
	}

	existing, err := s.modelRepo.ListByManagedBy(ctx, OrionManagedBy, tenantID)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	replaceAll := false
	for _, row := range existing {
		if row != nil && strings.TrimSpace(row.Parameters.APIKey) != apiKey {
			replaceAll = true
			break
		}
	}

	created, updated, deleted, err = s.modelRepo.ReconcileManaged(
		ctx, tenantID, OrionManagedBy, desired, replaceAll)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	if replaceAll {
		logger.Infof(ctx,
			"[orion-model] workspace %d rotated to the global key: %d previous rows replaced",
			tenantID, deleted)
	}

	s.ensureDefaults(ctx, tenantID, desired)
	return len(desired), created, updated, deleted, nil
}

// Status reports the workspace's sync state for the model page. The
// credential is platform-wide, so KeyConfigured is the same answer for every
// workspace.
func (s *OrionModelSync) Status(ctx context.Context, tenantID uint64) OrionSyncStatus {
	status := OrionSyncStatus{
		Enabled:             s.Enabled(),
		SyncIntervalSeconds: 300,
	}
	if s == nil {
		return status
	}
	if s.cfg != nil && s.cfg.SyncIntervalSeconds > 0 {
		status.SyncIntervalSeconds = s.cfg.SyncIntervalSeconds
	}

	s.mu.Lock()
	state := s.states[tenantID]
	s.mu.Unlock()
	status.LastSyncAt = state.lastSyncAt
	status.LastError = state.lastError

	if s.modelRepo != nil && tenantID != 0 {
		if rows, err := s.modelRepo.ListByManagedBy(ctx, OrionManagedBy, tenantID); err == nil {
			status.ModelCount = len(rows)
		}
	}
	if s.credentialStore != nil {
		if configured, err := s.credentialStore.Configured(ctx); err == nil {
			status.KeyConfigured = configured
		}
	}
	return status
}

// desiredModels maps the catalogue onto the rows this workspace should have.
func (s *OrionModelSync) desiredModels(
	ctx context.Context, tenantID uint64, apiKey string,
	entries []orion.ModelCatalogEntry, probeCache map[string]int,
) []*types.Model {
	out := make([]*types.Model, 0, len(entries))
	for _, entry := range entries {
		if !entry.Available {
			logger.Debugf(ctx,
				"[orion-model] skipping unavailable model %s (workspace %d)", entry.Name, tenantID)
			continue
		}
		switch entry.Type {
		case orion.CatalogTypeEmbedding:
			model := s.newRow(tenantID, apiKey, entry, types.ModelTypeEmbedding, "emb", "")
			if model == nil {
				continue
			}
			dimension := s.embeddingDimension(ctx, apiKey, entry.Name, probeCache)
			if dimension <= 0 {
				logger.Warnf(ctx,
					"[orion-model] embedding model %s skipped: dimension unknown (workspace %d)",
					entry.Name, tenantID)
				continue
			}
			model.Parameters.EmbeddingParameters.Dimension = dimension
			model.Parameters.SupportsVision = false
			model.Description = "Synced from Orion"
			out = append(out, model)
		case orion.CatalogTypeRerank:
			if model := s.newRow(tenantID, apiKey, entry, types.ModelTypeRerank, "rer", ""); model != nil {
				out = append(out, model)
			}
		case orion.CatalogTypeASR:
			if model := s.newRow(tenantID, apiKey, entry, types.ModelTypeASR, "asr", ""); model != nil {
				out = append(out, model)
			}
		case orion.CatalogTypeChat:
			if model := s.newRow(tenantID, apiKey, entry, types.ModelTypeKnowledgeQA, "chat", ""); model != nil {
				out = append(out, model)
			}
			// Orion has no dedicated vision type; vision is a WeKnora model
			// type served by the same /chat/completions endpoint.
			if s.cfg.SyncVision {
				if model := s.newRow(tenantID, apiKey, entry, types.ModelTypeVLLM, "vlm", " (Vision)"); model != nil {
					out = append(out, model)
				}
			}
		case "vlm":
			if model := s.newRow(tenantID, apiKey, entry, types.ModelTypeVLLM, "vlm", ""); model != nil {
				out = append(out, model)
			}
		default:
			logger.Debugf(ctx,
				"[orion-model] skipping model %s with unsupported type %q", entry.Name, entry.Type)
		}
	}
	return out
}

// inferenceBaseURL is the OpenAI-compatible base ("{root}/v1") that chat,
// embedding, rerank and ASR clients append their paths to. The root is the
// shared Orion gateway origin.
func (s *OrionModelSync) inferenceBaseURL() string {
	return strings.TrimRight(strings.TrimSpace(s.baseURL), "/") + "/v1"
}

// newRow builds one un-persisted model row for a catalogue entry.
func (s *OrionModelSync) newRow(
	tenantID uint64, apiKey string, entry orion.ModelCatalogEntry,
	modelType types.ModelType, typeTag, displaySuffix string,
) *types.Model {
	displayName := entry.DisplayName
	if displayName == "" {
		displayName = entry.Name
	}
	return &types.Model{
		ID:          OrionModelID(tenantID, typeTag, entry.Name),
		TenantID:    tenantID,
		Name:        entry.Name,
		DisplayName: displayName + displaySuffix,
		Type:        modelType,
		Source:      types.ModelSourceOrion,
		Description: "Synced from Orion",
		Parameters: types.ModelParameters{
			BaseURL:         s.inferenceBaseURL(),
			APIKey:          apiKey,
			Provider:        providers.OrionID,
			ContextWindow:   entry.ContextWindow,
			MaxOutputTokens: entry.MaxTokens,
			ExtraConfig:     map[string]string{"remote_model_name": entry.Name},
		},
		IsBuiltin: false,
		ManagedBy: OrionManagedBy,
		Status:    types.ModelStatusActive,
	}
}

// embeddingDimension resolves the vector width without guessing:
// existing row → WeKnora's built-in catalogue → one live probe (memoised per
// sweep through probeCache).
func (s *OrionModelSync) embeddingDimension(
	ctx context.Context, apiKey, modelName string, probeCache map[string]int,
) int {
	if dimension, ok := catalogEmbeddingDimension(modelName); ok {
		return dimension
	}
	if probeCache != nil {
		if dimension, ok := probeCache[modelName]; ok {
			return dimension
		}
	}
	dimension, err := s.catalog.ProbeEmbeddingDimension(ctx, apiKey, modelName)
	if err != nil {
		logger.Warnf(ctx,
			"[orion-model] probing embedding dimension for %s failed: %v", modelName, err)
		return 0
	}
	if probeCache != nil {
		probeCache[modelName] = dimension
	}
	return dimension
}

// ensureDefaults gives every model type one default the first time the
// workspace has no choice, so a fresh workspace can immediately create a
// knowledge base. Tenant-level DefaultSettings remains authoritative.
func (s *OrionModelSync) ensureDefaults(ctx context.Context, tenantID uint64, desired []*types.Model) {
	byType := make(map[types.ModelType][]*types.Model)
	for _, row := range desired {
		byType[row.Type] = append(byType[row.Type], row)
	}
	for modelType, rows := range byType {
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
		current, err := s.modelRepo.List(ctx, tenantID, modelType, types.ModelSourceOrion)
		if err != nil || len(current) == 0 {
			continue
		}
		hasDefault := false
		for _, row := range current {
			if row.IsDefault {
				hasDefault = true
				break
			}
		}
		if hasDefault {
			continue
		}
		pick := current[0]
		for _, row := range current {
			if row.Name < pick.Name {
				pick = row
			}
		}
		pick.IsDefault = true
		if err := s.modelRepo.ClearDefaultByType(ctx, uint(tenantID), modelType, pick.ID); err != nil {
			logger.Warnf(ctx,
				"[orion-model] workspace %d clearing %s defaults failed: %v", tenantID, modelType, err)
			continue
		}
		if err := s.modelRepo.Update(ctx, pick); err != nil {
			logger.Warnf(ctx,
				"[orion-model] workspace %d setting %s default failed: %v", tenantID, modelType, err)
		}
	}
}

func (s *OrionModelSync) begin(tenantID uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running == nil {
		s.running = make(map[uint64]struct{})
	}
	if _, busy := s.running[tenantID]; busy {
		return false
	}
	s.running[tenantID] = struct{}{}
	return true
}

func (s *OrionModelSync) end(tenantID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, tenantID)
}

func (s *OrionModelSync) recordSuccess(tenantID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.states == nil {
		s.states = make(map[uint64]orionTenantState)
	}
	s.states[tenantID] = orionTenantState{lastSyncAt: time.Now()}
}

func (s *OrionModelSync) recordFailure(tenantID uint64, err error) {
	message := ""
	if err != nil {
		message = err.Error()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.states == nil {
		s.states = make(map[uint64]orionTenantState)
	}
	state := s.states[tenantID]
	// Keep the last successful timestamp: "last synced 3 minutes ago" is still
	// true and more useful than blanking it on a transient failure.
	state.lastError = message
	s.states[tenantID] = state
}

// OrionModelID derives a stable, URL-safe primary key. models.id is a global
// varchar(64) primary key, so the workspace and model type are part of the id
// and an over-long name is truncated with a hash suffix.
func OrionModelID(tenantID uint64, typeTag, modelName string) string {
	base := fmt.Sprintf("orion-%d-%s-%s", tenantID, typeTag, sanitizeModelIDPart(modelName))
	if len(base) <= types.ModelIDMaxLen {
		return base
	}
	sum := sha256.Sum256([]byte(base))
	suffix := hex.EncodeToString(sum[:4])
	keep := types.ModelIDMaxLen - len(suffix) - 1
	if keep < 1 {
		keep = 1
	}
	return base[:keep] + "-" + suffix
}

// sanitizeModelIDPart keeps ids safe for URL path segments and query strings.
func sanitizeModelIDPart(value string) string {
	var out strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			out.WriteRune(r)
		default:
			out.WriteByte('_')
		}
	}
	sanitized := out.String()
	if sanitized == "" {
		return "model"
	}
	return sanitized
}

// catalogEmbeddingDimension finds the vector width WeKnora already knows for
// this model family. Orion names carry serving suffixes ("bge-m3-Q8_0") while
// the catalogue uses vendor-prefixed ids ("BAAI/bge-m3"), so matching is done on
// the normalised base name.
func catalogEmbeddingDimension(modelName string) (int, bool) {
	target := normalizeModelName(modelName)
	if target == "" {
		return 0, false
	}
	for _, provider := range runtime.Default().List() {
		for _, spec := range provider.Models() {
			if spec.Type != types.ModelTypeEmbedding || spec.Dimension <= 0 {
				continue
			}
			if normalizeModelName(spec.ID) == target || normalizeModelName(spec.Name) == target {
				return spec.Dimension, true
			}
			for _, alias := range spec.Aliases {
				if normalizeModelName(alias) == target {
					return spec.Dimension, true
				}
			}
		}
	}
	return 0, false
}

// normalizeModelName lowercases, drops the vendor prefix and strips the common
// serving/quantisation suffixes.
func normalizeModelName(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if idx := strings.LastIndex(normalized, "/"); idx >= 0 {
		normalized = normalized[idx+1:]
	}
	for _, suffix := range []string{
		"-q8_0", "-q8-k", "-q6_k", "-q5_k_m", "-q5_0", "-q4_k_m", "-q4_0",
		"-fp16", "-bf16", "-int8", "-int4",
	} {
		normalized = strings.TrimSuffix(normalized, suffix)
	}
	return normalized
}
