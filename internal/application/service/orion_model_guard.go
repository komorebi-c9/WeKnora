package service

import (
	"context"

	"github.com/Tencent/WeKnora/internal/config"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/models/asr"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/models/embedding"
	"github.com/Tencent/WeKnora/internal/models/rerank"
	"github.com/Tencent/WeKnora/internal/models/vlm"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// orionAwareModelService enforces the two halves of the Orion contract without
// touching the existing model service:
//
//   - switch on: only managed_by=orion rows of the caller's workspace are
//     visible or usable, and every write is rejected;
//   - switch off: Orion rows are hidden (and unusable) so the original model
//     management experience is exactly as it was, while the synced rows stay in
//     the database untouched.
//
// It is a decorator because NewModelService is called directly by a large body
// of unit tests; changing its signature would churn all of them.
type orionAwareModelService struct {
	interfaces.ModelService
	cfg  *config.OrionModelConfig
	repo interfaces.ModelRepository
}

// NewOrionAwareModelService wraps inner with the Orion visibility/write policy.
func NewOrionAwareModelService(
	inner interfaces.ModelService,
	cfg *config.OrionModelConfig,
	repo interfaces.ModelRepository,
) interfaces.ModelService {
	if inner == nil {
		return nil
	}
	if cfg == nil {
		cfg = &config.OrionModelConfig{}
	}
	if repo == nil {
		// Without a repository the policy cannot be evaluated; leaving the
		// service untouched is the only safe option (it also keeps tests that
		// build the service by hand working).
		return inner
	}
	return &orionAwareModelService{ModelService: inner, cfg: cfg, repo: repo}
}

func (s *orionAwareModelService) enabled() bool {
	return s.cfg.IsEnabled()
}

func orionReadOnlyModelError() error {
	return apperrors.NewForbiddenError(
		"models are managed by Orion in this deployment; add or change models in Orion",
	)
}

// ListModels keeps the list consistent with the active mode.
func (s *orionAwareModelService) ListModels(ctx context.Context) ([]*types.Model, error) {
	models, err := s.ModelService.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	tenantID := types.MustTenantIDFromContext(ctx)
	filtered := make([]*types.Model, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		owned := model.ManagedBy == OrionManagedBy
		if s.enabled() {
			if owned && model.TenantID == tenantID {
				filtered = append(filtered, model)
			}
			continue
		}
		if !owned {
			filtered = append(filtered, model)
		}
	}
	return filtered, nil
}

// GetModelByID hides rows the current mode does not own.
func (s *orionAwareModelService) GetModelByID(ctx context.Context, id string) (*types.Model, error) {
	model, err := s.ModelService.GetModelByID(ctx, id)
	if err != nil || model == nil {
		return model, err
	}
	if err := s.checkRow(ctx, model); err != nil {
		return nil, err
	}
	return model, nil
}

// CreateModel is rejected while Orion owns the catalogue.
func (s *orionAwareModelService) CreateModel(ctx context.Context, model *types.Model) error {
	if s.enabled() {
		return orionReadOnlyModelError()
	}
	return s.ModelService.CreateModel(ctx, model)
}

// CopyModel is rejected while Orion owns the catalogue.
func (s *orionAwareModelService) CopyModel(
	ctx context.Context, sourceID, displayName string,
) (*types.Model, error) {
	if s.enabled() {
		return nil, orionReadOnlyModelError()
	}
	return s.ModelService.CopyModel(ctx, sourceID, displayName)
}

// UpdateModel is rejected while Orion owns the catalogue.
func (s *orionAwareModelService) UpdateModel(ctx context.Context, model *types.Model) error {
	if s.enabled() {
		return orionReadOnlyModelError()
	}
	return s.ModelService.UpdateModel(ctx, model)
}

// DeleteModel is rejected while Orion owns the catalogue.
func (s *orionAwareModelService) DeleteModel(ctx context.Context, id string) error {
	if s.enabled() {
		return orionReadOnlyModelError()
	}
	return s.ModelService.DeleteModel(ctx, id)
}

// UpdateModelCredentials is rejected while Orion owns the catalogue: the key
// belongs to the user row, not to the model row.
func (s *orionAwareModelService) UpdateModelCredentials(
	ctx context.Context, id string, apiKey, appSecret *string,
) (*types.Model, error) {
	if s.enabled() {
		return nil, orionReadOnlyModelError()
	}
	return s.ModelService.UpdateModelCredentials(ctx, id, apiKey, appSecret)
}

// ClearModelCredential is rejected while Orion owns the catalogue.
func (s *orionAwareModelService) ClearModelCredential(
	ctx context.Context, id, field string,
) error {
	if s.enabled() {
		return orionReadOnlyModelError()
	}
	return s.ModelService.ClearModelCredential(ctx, id, field)
}

// GetEmbeddingModel only resolves rows the active mode owns.
func (s *orionAwareModelService) GetEmbeddingModel(
	ctx context.Context, modelID string,
) (embedding.Embedder, error) {
	if err := s.ensureUsable(ctx, modelID); err != nil {
		return nil, err
	}
	return s.ModelService.GetEmbeddingModel(ctx, modelID)
}

// GetEmbeddingModelForTenant checks ownership against the tenant the knowledge
// base belongs to, not the caller's active workspace.
func (s *orionAwareModelService) GetEmbeddingModelForTenant(
	ctx context.Context, modelID string, tenantID uint64,
) (embedding.Embedder, error) {
	if err := s.ensureUsableForTenant(ctx, modelID, tenantID); err != nil {
		return nil, err
	}
	return s.ModelService.GetEmbeddingModelForTenant(ctx, modelID, tenantID)
}

// GetRerankModel only resolves rows the active mode owns.
func (s *orionAwareModelService) GetRerankModel(
	ctx context.Context, modelID string,
) (rerank.Reranker, error) {
	if err := s.ensureUsable(ctx, modelID); err != nil {
		return nil, err
	}
	return s.ModelService.GetRerankModel(ctx, modelID)
}

// GetChatModel only resolves rows the active mode owns.
func (s *orionAwareModelService) GetChatModel(
	ctx context.Context, modelID string,
) (chat.Chat, error) {
	if err := s.ensureUsable(ctx, modelID); err != nil {
		return nil, err
	}
	return s.ModelService.GetChatModel(ctx, modelID)
}

// GetVLMModel only resolves rows the active mode owns.
func (s *orionAwareModelService) GetVLMModel(
	ctx context.Context, modelID string,
) (vlm.VLM, error) {
	if err := s.ensureUsable(ctx, modelID); err != nil {
		return nil, err
	}
	return s.ModelService.GetVLMModel(ctx, modelID)
}

// GetASRModel only resolves rows the active mode owns.
func (s *orionAwareModelService) GetASRModel(
	ctx context.Context, modelID string,
) (asr.ASR, error) {
	if err := s.ensureUsable(ctx, modelID); err != nil {
		return nil, err
	}
	return s.ModelService.GetASRModel(ctx, modelID)
}

// ensureUsable rejects a model id that belongs to the other mode.
func (s *orionAwareModelService) ensureUsable(ctx context.Context, modelID string) error {
	return s.ensureUsableForTenant(ctx, modelID, types.MustTenantIDFromContext(ctx))
}

func (s *orionAwareModelService) ensureUsableForTenant(
	ctx context.Context, modelID string, tenantID uint64,
) error {
	if modelID == "" {
		return ErrModelNotFound
	}
	model, err := s.repo.GetByID(ctx, tenantID, modelID)
	if err != nil {
		return err
	}
	if model == nil {
		return ErrModelNotFound
	}
	return s.checkRowForTenant(model, tenantID)
}

// checkRow applies the visibility rule to an already loaded row.
func (s *orionAwareModelService) checkRow(ctx context.Context, model *types.Model) error {
	return s.checkRowForTenant(model, types.MustTenantIDFromContext(ctx))
}

func (s *orionAwareModelService) checkRowForTenant(model *types.Model, tenantID uint64) error {
	if model == nil {
		return ErrModelNotFound
	}
	owned := model.ManagedBy == OrionManagedBy && model.TenantID == tenantID
	if s.enabled() {
		if !owned {
			return ErrModelNotFound
		}
		return nil
	}
	if model.ManagedBy == OrionManagedBy {
		return ErrModelNotFound
	}
	return nil
}
