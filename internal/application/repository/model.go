package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// modelRepository implements the model repository interface
type modelRepository struct {
	db *gorm.DB
}

// NewModelRepository creates a new model repository
func NewModelRepository(db *gorm.DB) interfaces.ModelRepository {
	return &modelRepository{db: db}
}

// Create creates a new model
func (r *modelRepository) Create(ctx context.Context, m *types.Model) error {
	return r.db.WithContext(ctx).Create(m).Error
}

// GetByID retrieves a model by ID
func (r *modelRepository) GetByID(ctx context.Context, tenantID uint64, id string) (*types.Model, error) {
	var m types.Model
	if err := r.db.WithContext(ctx).Where("id = ?", id).Where(
		"(tenant_id = ? OR is_builtin = true)", tenantID,
	).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &m, nil
}

// List lists models with optional filtering
func (r *modelRepository) List(
	ctx context.Context, tenantID uint64, modelType types.ModelType, source types.ModelSource,
) ([]*types.Model, error) {
	var models []*types.Model
	query := r.db.WithContext(ctx).Where(
		"(tenant_id = ? OR is_builtin = true)", tenantID,
	)

	if modelType != "" {
		query = query.Where("type = ?", modelType)
	}

	if source != "" {
		query = query.Where("source = ?", source)
	}

	if err := query.Find(&models).Error; err != nil {
		return nil, err
	}

	return models, nil
}

// Update updates a model
func (r *modelRepository) Update(ctx context.Context, m *types.Model) error {
	// Use Select to explicitly update all fields, including zero values like false
	return r.db.WithContext(ctx).Debug().Model(&types.Model{}).Where(
		"id = ? AND tenant_id = ?", m.ID, m.TenantID,
	).Select("*").Updates(m).Error
}

// Delete deletes a model
func (r *modelRepository) Delete(ctx context.Context, tenantID uint64, id string) error {
	return r.db.WithContext(ctx).Where(
		"id = ? AND tenant_id = ?", id, tenantID,
	).Delete(&types.Model{}).Error
}

// ListByManagedBy returns every model row owned by managedBy. Rows soft-deleted
// by a previous sync are excluded: they no longer belong to the live catalogue.
func (r *modelRepository) ListByManagedBy(
	ctx context.Context, managedBy string, tenantID uint64,
) ([]*types.Model, error) {
	if managedBy == "" {
		return nil, errors.New("managed_by is required")
	}
	query := r.db.WithContext(ctx).Where("managed_by = ?", managedBy)
	if tenantID != 0 {
		query = query.Where("tenant_id = ?", tenantID)
	}
	var models []*types.Model
	if err := query.Find(&models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// UpsertManaged inserts or updates one synchroniser-owned row.
//
// Ownership is checked first: a primary-key collision with a row owned by a
// different subsystem is an error rather than a silent overwrite, because the
// manual row may be referenced by knowledge bases. The upsert also clears
// deleted_at so a model that Orion re-lists comes back with its original id
// (and therefore its existing bindings) instead of a duplicate.
func (r *modelRepository) UpsertManaged(
	ctx context.Context, model *types.Model, managedBy string,
) error {
	_, err := upsertManagedSession(r.db.WithContext(ctx), model, managedBy)
	return err
}

// upsertManagedSession runs the managed upsert against one GORM session —
// either the plain context-bound handle or an open transaction. It returns
// true when the row was newly created (absent or soft-deleted beforehand)
// and false when an existing live row was updated.
func upsertManagedSession(session *gorm.DB, model *types.Model, managedBy string) (bool, error) {
	if model == nil {
		return false, errors.New("model is nil")
	}
	if managedBy == "" {
		return false, errors.New("managed_by is required")
	}

	var existing types.Model
	err := session.Unscoped().
		Select("id", "managed_by", "deleted_at").
		Where("id = ?", model.ID).
		First(&existing).Error
	switch {
	case err == nil:
		if existing.ManagedBy != managedBy {
			return false, fmt.Errorf(
				"model %q is managed by %q, refusing to overwrite it", model.ID, existing.ManagedBy,
			)
		}
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return false, err
	}

	model.ManagedBy = managedBy
	err = session.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		// created_at is intentionally absent: the first sync owns it, later
		// syncs must not rewrite history. is_default is absent too — the
		// synchroniser decides defaults with ClearDefaultByType so a workspace
		// admin's explicit choice is not clobbered on every refresh.
		DoUpdates: clause.AssignmentColumns([]string{
			"tenant_id",
			"name",
			"display_name",
			"type",
			"source",
			"description",
			"parameters",
			"is_builtin",
			"managed_by",
			"status",
			"updated_at",
			"deleted_at",
		}),
	}).Create(model).Error
	if err != nil {
		return false, err
	}
	return existing.ID == "" || existing.DeletedAt.Valid, nil
}

// ReconcileManaged atomically replaces the managedBy-owned rows of one
// workspace with the desired set.
//
// The whole read-modify-write runs in a single database transaction:
//   - replaceAll (credential rotation): every previous managedBy row of the
//     workspace is soft-deleted, then the desired rows are upserted — old
//     models must not survive a key change, and the swap must never expose
//     an intermediate "no models" window to readers;
//   - otherwise (regular diff): desired rows are upserted and rows absent
//     from desired are pruned.
//
// Any failure rolls the transaction back, leaving the workspace's model set
// exactly as it was before the call.
func (r *modelRepository) ReconcileManaged(
	ctx context.Context, tenantID uint64, managedBy string,
	desired []*types.Model, replaceAll bool,
) (created, updated, deleted int, err error) {
	if managedBy == "" {
		return 0, 0, 0, errors.New("managed_by is required")
	}

	txErr := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		created, updated, deleted = 0, 0, 0

		if replaceAll {
			res := tx.Where("tenant_id = ? AND managed_by = ?", tenantID, managedBy).
				Delete(&types.Model{})
			if res.Error != nil {
				return res.Error
			}
			deleted = int(res.RowsAffected)
		}

		keep := make(map[string]struct{}, len(desired))
		for _, want := range desired {
			if want == nil {
				continue
			}
			keep[want.ID] = struct{}{}
			wasCreated, err := upsertManagedSession(tx, want, managedBy)
			if err != nil {
				return err
			}
			if wasCreated {
				created++
			} else {
				updated++
			}
		}

		if replaceAll {
			// The deleted counter above already covers the wipe; rows were
			// recreated by the upserts, so nothing else to prune here.
			return nil
		}

		var stale []*types.Model
		if err := tx.Where("tenant_id = ? AND managed_by = ?", tenantID, managedBy).
			Find(&stale).Error; err != nil {
			return err
		}
		for _, row := range stale {
			if _, ok := keep[row.ID]; ok {
				continue
			}
			if err := tx.Where("id = ? AND tenant_id = ?", row.ID, tenantID).
				Delete(&types.Model{}).Error; err != nil {
				return err
			}
			deleted++
		}
		return nil
	})
	if txErr != nil {
		return 0, 0, 0, txErr
	}
	return created, updated, deleted, nil
}

// ClearDefaultByType clears the default flag for all models of a specific type
// This is a batch operation that updates all matching records in one query
func (r *modelRepository) ClearDefaultByType(
	ctx context.Context,
	tenantID uint,
	modelType types.ModelType,
	excludeID string,
) error {
	query := r.db.WithContext(ctx).Model(&types.Model{}).Where(
		"tenant_id = ? AND type = ? AND is_default = ?", tenantID, modelType, true,
	)

	// If excludeID is provided, exclude that model from the update
	if excludeID != "" {
		query = query.Where("id != ?", excludeID)
	}

	// Batch update: set is_default to false for all matching records
	return query.Update("is_default", false).Error
}
