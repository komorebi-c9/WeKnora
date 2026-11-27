package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type stubKnowledgePager struct {
	interfaces.KnowledgeService
	total int64
}

func (s *stubKnowledgePager) ListPagedKnowledgeByKnowledgeBaseID(
	context.Context, string, *types.Pagination, types.KnowledgeListFilter,
) (*types.PageResult, error) {
	return &types.PageResult{Total: s.total}, nil
}

type stubEmbeddingSwitchKBService struct {
	interfaces.KnowledgeBaseService
	kb      *types.KnowledgeBase
	rebuilt []string
}

func (s *stubEmbeddingSwitchKBService) GetKnowledgeBaseByID(
	context.Context, string,
) (*types.KnowledgeBase, error) {
	return s.kb, nil
}

func (s *stubEmbeddingSwitchKBService) RebuildEmbeddingIndex(_ context.Context, id string) error {
	s.rebuilt = append(s.rebuilt, id)
	return nil
}

// PUT /initialization/config is the KB settings save path. When the pinned
// embedding model can no longer be resolved the knowledge base is already
// broken, so the switch must be allowed and a rebuild scheduled; otherwise the
// workspace is trapped with no way to repair it.
func TestUpdateKBConfigAllowsSwitchFromUnresolvableEmbedding(t *testing.T) {
	kb := &types.KnowledgeBase{ID: "kb-1", TenantID: 7, EmbeddingModelID: "old-emb"}
	repo := &stubInitializationKBRepo{}
	kbService := &stubEmbeddingSwitchKBService{kb: kb}
	models := &stubTenantStampModelService{getModelByID: func(_ context.Context, id string) (*types.Model, error) {
		if id == "old-emb" {
			return nil, nil // the row is gone
		}
		return &types.Model{ID: id, TenantID: 7, Type: types.ModelTypeEmbedding}, nil
	}}
	knowledge := &stubKnowledgePager{total: 3}
	h := &InitializationHandler{
		kbService:        kbService,
		kbRepository:     repo,
		modelService:     models,
		knowledgeService: knowledge,
	}

	c := newInitializationConfigContext(t, "kb-1", `{"llmModelId":"m-llm","embeddingModelId":"new-emb"}`)
	h.UpdateKBConfig(c)

	require.Empty(t, c.Errors)
	require.NotNil(t, repo.updated)
	require.Equal(t, "new-emb", repo.updated.EmbeddingModelID)
	require.Equal(t, []string{"kb-1"}, kbService.rebuilt,
		"documents must be re-embedded with the new model")
}

// The same request is still refused while the pinned model resolves: documents
// pin the vectors they were indexed with.
func TestUpdateKBConfigRejectsSwitchFromResolvableEmbedding(t *testing.T) {
	kb := &types.KnowledgeBase{ID: "kb-1", TenantID: 7, EmbeddingModelID: "old-emb"}
	repo := &stubInitializationKBRepo{}
	kbService := &stubEmbeddingSwitchKBService{kb: kb}
	models := &stubTenantStampModelService{getModelByID: func(_ context.Context, id string) (*types.Model, error) {
		return &types.Model{ID: id, TenantID: 7, Type: types.ModelTypeEmbedding}, nil
	}}
	h := &InitializationHandler{
		kbService:        kbService,
		kbRepository:     repo,
		modelService:     models,
		knowledgeService: &stubKnowledgePager{total: 3},
	}

	c := newInitializationConfigContext(t, "kb-1", `{"llmModelId":"m-llm","embeddingModelId":"new-emb"}`)
	h.UpdateKBConfig(c)

	require.Len(t, c.Errors, 1)
	require.Contains(t, c.Errors[0].Err.Error(), "无法修改Embedding模型")
	require.Nil(t, repo.updated, "the KB must not be written")
	require.Empty(t, kbService.rebuilt)
}

func newInitializationConfigContext(t *testing.T, kbID, body string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Params = gin.Params{{Key: "kbId", Value: kbID}}
	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	caller := types.Caller{TenantID: 7, UserID: "admin", Role: types.TenantRoleAdmin}
	c.Request = req.WithContext(types.WithExecutionTenant(types.WithCaller(req.Context(), caller), 7))
	return c
}
