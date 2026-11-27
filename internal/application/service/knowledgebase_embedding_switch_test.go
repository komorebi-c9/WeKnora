package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type embeddingSwitchKBRepo struct {
	interfaces.KnowledgeBaseRepository
	kb    types.KnowledgeBase
	saves int
}

func (r *embeddingSwitchKBRepo) GetKnowledgeBaseByID(context.Context, string) (*types.KnowledgeBase, error) {
	kb := r.kb
	return &kb, nil
}

func (r *embeddingSwitchKBRepo) UpdateKnowledgeBase(_ context.Context, kb *types.KnowledgeBase) error {
	r.kb = *kb
	r.saves++
	return nil
}

type embeddingSwitchKnowledgeRepo struct {
	interfaces.KnowledgeRepository
	total      int64
	countErr   error
	knowledges []*types.Knowledge
	listErr    error
	listCalls  int
}

func (r *embeddingSwitchKnowledgeRepo) CountKnowledgeByKnowledgeBaseID(
	context.Context, uint64, string,
) (int64, error) {
	return r.total, r.countErr
}

func (r *embeddingSwitchKnowledgeRepo) ListKnowledgeByKnowledgeBaseID(
	context.Context, uint64, string,
) ([]*types.Knowledge, error) {
	r.listCalls++
	return r.knowledges, r.listErr
}

type embeddingSwitchModels struct {
	interfaces.ModelService
	models map[string]*types.Model
}

func (m *embeddingSwitchModels) GetModelByID(_ context.Context, id string) (*types.Model, error) {
	if model, ok := m.models[id]; ok {
		return model, nil
	}
	return nil, ErrModelNotFound
}

type embeddingSwitchEnqueuer struct {
	tasks []*asynq.Task
	err   error
}

func (e *embeddingSwitchEnqueuer) Enqueue(task *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	if e.err != nil {
		return nil, e.err
	}
	e.tasks = append(e.tasks, task)
	return &asynq.TaskInfo{ID: "task-id"}, nil
}

func newEmbeddingSwitchService() (
	*knowledgeBaseService, *embeddingSwitchKBRepo, *embeddingSwitchKnowledgeRepo, *embeddingSwitchEnqueuer,
) {
	kbRepo := &embeddingSwitchKBRepo{kb: types.KnowledgeBase{
		ID:               "kb-1",
		TenantID:         1,
		Name:             "kb",
		Type:             types.KnowledgeBaseTypeDocument,
		EmbeddingModelID: "old-embed",
	}}
	kgRepo := &embeddingSwitchKnowledgeRepo{
		total: 2,
		knowledges: []*types.Knowledge{
			{ID: "knowledge-1", TenantID: 1, KnowledgeBaseID: "kb-1"},
			{ID: "knowledge-2", TenantID: 1, KnowledgeBaseID: "kb-1"},
		},
	}
	models := &embeddingSwitchModels{models: map[string]*types.Model{
		"new-embed": {ID: "new-embed", TenantID: 1, Type: types.ModelTypeEmbedding},
		"old-embed": {ID: "old-embed", TenantID: 1, Type: types.ModelTypeEmbedding},
		"chat":      {ID: "chat", TenantID: 1, Type: types.ModelTypeKnowledgeQA},
	}}
	enqueuer := &embeddingSwitchEnqueuer{}
	svc := &knowledgeBaseService{
		repo:         kbRepo,
		kgRepo:       kgRepo,
		modelService: models,
		asynqClient:  enqueuer,
	}
	return svc, kbRepo, kgRepo, enqueuer
}

func embeddingPtr(id string) *string { return &id }

// A live model keeps the original rule: documents pin the vectors they were
// indexed with, so the switch is refused and nothing is written or queued.
func TestUpdateKnowledgeBase_RejectsEmbeddingSwitchWhenOldModelIsAlive(t *testing.T) {
	svc, kbRepo, kgRepo, enqueuer := newEmbeddingSwitchService()

	updated, err := svc.UpdateKnowledgeBase(ctxWithTenant(1), "kb-1", "kb", "",
		&types.KnowledgeBaseConfig{EmbeddingModelID: embeddingPtr("new-embed")}, nil)

	require.Error(t, err)
	require.Nil(t, updated)
	appErr, ok := apperrors.IsAppError(err)
	require.True(t, ok)
	require.Equal(t, http.StatusBadRequest, appErr.HTTPCode)
	require.Zero(t, kbRepo.saves)
	require.Equal(t, "old-embed", kbRepo.kb.EmbeddingModelID)
	require.Empty(t, enqueuer.tasks)
	require.Zero(t, kgRepo.listCalls)
}

// The dead end this test pins: the catalogue rotated and the old embedding
// model row is gone. Refusing the switch would leave every document
// unparseable, so the switch must land and the index must be rebuilt.
func TestUpdateKnowledgeBase_AllowsEmbeddingSwitchWhenOldModelIsGone(t *testing.T) {
	svc, kbRepo, kgRepo, enqueuer := newEmbeddingSwitchService()
	svc.modelService.(*embeddingSwitchModels).models = map[string]*types.Model{
		"new-embed": {ID: "new-embed", TenantID: 1, Type: types.ModelTypeEmbedding},
	}
	// A document mid-move is rebuilt by the transfer itself, so it is skipped;
	// unreadable metadata must not cost a document its re-embedding.
	kgRepo.knowledges = append(kgRepo.knowledges,
		&types.Knowledge{
			ID: "knowledge-moving", TenantID: 1, KnowledgeBaseID: "kb-1",
			Metadata: types.JSON(`{"_knowledge_transfer":{"operation":"move","phase":"moving"}}`),
		},
		&types.Knowledge{
			ID: "knowledge-badmeta", TenantID: 1, KnowledgeBaseID: "kb-1",
			Metadata: types.JSON(`{not json`),
		},
	)

	updated, err := svc.UpdateKnowledgeBase(ctxWithTenant(1), "kb-1", "kb", "",
		&types.KnowledgeBaseConfig{EmbeddingModelID: embeddingPtr("new-embed")}, nil)

	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, "new-embed", updated.EmbeddingModelID)
	require.Equal(t, 1, kbRepo.saves)
	require.Equal(t, 1, kgRepo.listCalls, "every document has to be re-indexed")
	require.Len(t, enqueuer.tasks, 1)

	task := enqueuer.tasks[0]
	require.Equal(t, types.TypeKnowledgeListReparse, task.Type())
	var payload types.KnowledgeListReparsePayload
	require.NoError(t, json.Unmarshal(task.Payload(), &payload))
	require.Equal(t, "kb-1", payload.KnowledgeBaseID)
	require.Equal(t, []string{"knowledge-1", "knowledge-2", "knowledge-badmeta"}, payload.KnowledgeIDs)
}

// An empty knowledge base never needed a rebuild, so the switch is free.
func TestUpdateKnowledgeBase_EmbeddingSwitchOnEmptyKBDoesNotRebuild(t *testing.T) {
	svc, kbRepo, kgRepo, enqueuer := newEmbeddingSwitchService()
	kgRepo.total = 0
	kgRepo.knowledges = nil

	updated, err := svc.UpdateKnowledgeBase(ctxWithTenant(1), "kb-1", "kb", "",
		&types.KnowledgeBaseConfig{EmbeddingModelID: embeddingPtr("new-embed")}, nil)

	require.NoError(t, err)
	require.Equal(t, "new-embed", updated.EmbeddingModelID)
	require.Equal(t, 1, kbRepo.saves)
	require.Zero(t, kgRepo.listCalls)
	require.Empty(t, enqueuer.tasks)
}

// The replacement still has to be an embedding model, even when the old row is
// gone: accepting a chat model here would surface much later, inside embedding.
func TestUpdateKnowledgeBase_EmbeddingSwitchChecksTheNewModelType(t *testing.T) {
	svc, kbRepo, _, enqueuer := newEmbeddingSwitchService()
	svc.modelService.(*embeddingSwitchModels).models = map[string]*types.Model{
		"chat": {ID: "chat", TenantID: 1, Type: types.ModelTypeKnowledgeQA},
	}

	_, err := svc.UpdateKnowledgeBase(ctxWithTenant(1), "kb-1", "kb", "",
		&types.KnowledgeBaseConfig{EmbeddingModelID: embeddingPtr("chat")}, nil)

	require.Error(t, err)
	appErr, ok := apperrors.IsAppError(err)
	require.True(t, ok)
	require.Equal(t, http.StatusBadRequest, appErr.HTTPCode)
	require.Zero(t, kbRepo.saves)
	require.Empty(t, enqueuer.tasks)
}

// The model is saved before the rebuild is queued. When the queue is down the
// caller has to learn that the rebuild did not start, but the persisted config
// must not be rolled back silently.
func TestUpdateKnowledgeBase_ReportsRebuildEnqueueFailure(t *testing.T) {
	svc, kbRepo, _, enqueuer := newEmbeddingSwitchService()
	svc.modelService.(*embeddingSwitchModels).models = map[string]*types.Model{
		"new-embed": {ID: "new-embed", TenantID: 1, Type: types.ModelTypeEmbedding},
	}
	queueErr := errors.New("redis unavailable")
	enqueuer.err = queueErr

	updated, err := svc.UpdateKnowledgeBase(ctxWithTenant(1), "kb-1", "kb", "",
		&types.KnowledgeBaseConfig{EmbeddingModelID: embeddingPtr("new-embed")}, nil)

	require.ErrorContains(t, err, "rebuild could not be scheduled")
	require.NotNil(t, updated)
	require.Equal(t, 1, kbRepo.saves)
	require.Equal(t, "new-embed", kbRepo.kb.EmbeddingModelID)
}
