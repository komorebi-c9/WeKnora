package runtime_test

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/models/api"
	modelruntime "github.com/Tencent/WeKnora/internal/models/runtime"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// testOrionResolve covers the rows the synchroniser writes: a provider id of
// "orion", a base URL of {root}/v1, and model names that WeKnora's catalog does
// not know. Resolution must still land on the OpenAI-compatible protocols the
// gateway actually serves — otherwise the synced rows would build clients for
// the wrong endpoint.
func testOrionResolvePerType(t *testing.T, modelName string, modelType types.ModelType) *modelruntime.Resolved {
	t.Helper()
	resolved, err := modelruntime.Resolve(modelruntime.Ref{
		Provider:  "orion",
		Model:     modelName,
		BaseURL:   "http://orion.local/orion/v1",
		ModelType: modelType,
		Extra:     map[string]string{"remote_model_name": modelName},
	})
	require.NoError(t, err, "%s/%s must resolve", modelType, modelName)
	require.Equal(t, "http://orion.local/orion/v1", resolved.BaseURL)
	require.Equal(t, modelName, resolved.RemoteModel)
	return resolved
}

func TestOrionChatRowsResolveToChatCompletions(t *testing.T) {
	for _, modelType := range []types.ModelType{types.ModelTypeKnowledgeQA, types.ModelTypeVLLM} {
		resolved := testOrionResolvePerType(t, "local_Qwen3.6-35B-A3B-MTP", modelType)
		require.Equal(t, api.APIOpenAICompletions, resolved.API)
	}
}

func TestOrionEmbeddingRowResolvesToEmbeddings(t *testing.T) {
	resolved := testOrionResolvePerType(t, "bge-m3-Q8_0", types.ModelTypeEmbedding)
	require.NotEmpty(t, resolved.EmbeddingAPI, "embedding rows need an embedding protocol")
}

func TestOrionRerankRowResolvesToRerank(t *testing.T) {
	resolved := testOrionResolvePerType(t, "bge-reranker-v2-m3-Q8_0", types.ModelTypeRerank)
	require.NotEmpty(t, resolved.RerankAPI, "rerank rows need a rerank protocol")
}

func TestOrionASRRowResolvesToTranscriptions(t *testing.T) {
	resolved := testOrionResolvePerType(t, "whisper-large-v3", types.ModelTypeASR)
	require.NotEmpty(t, resolved.TranscriptionAPI, "ASR rows need a transcription protocol")
}
