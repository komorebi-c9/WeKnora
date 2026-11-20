// Package providers registers the LoLR Orion model gateway.
//
// Facts (verified against the lolr-orion sources):
//   - Orion exposes an OpenAI-compatible inference face under `{root}/v1`:
//     POST /chat/completions, /embeddings, /rerank. SSE is passed through
//     unchanged, so the OpenAI Chat Completions protocol is the right fit.
//   - The request body is forwarded to the selected upstream provider
//     essentially as-is; `model` may be the bare model name or
//     "provider_name/model_name".
//   - /audio/transcriptions is not implemented yet (ASR rows are registered in
//     Orion's catalogue but the gateway has no route). The ASR model type is
//     still declared here so a speech model can be synced and starts working
//     the moment Orion ships the route.
//   - Authentication is the caller's Orion License SK. WeKnora stores it per
//     user (users.orion_api_key) and writes it on the model row, so the
//     standard Bearer convention applies.
package providers

import (
	_ "embed"

	"github.com/Tencent/WeKnora/internal/models"
	"github.com/Tencent/WeKnora/internal/models/api"
	"github.com/Tencent/WeKnora/internal/types"
)

//go:embed assets/orion.svg
var orionIcon []byte

// OrionID is the provider identifier stored on model rows and referenced by
// source=orion rows created by the synchroniser.
const OrionID = "orion"

// OrionBaseURL is a placeholder the synchroniser overwrites with
// {ORION_BASE_URL}/v1. It is only used when an operator creates a row by
// hand (which Orion mode forbids) or reads provider defaults in the UI.
const OrionBaseURL = "http://localhost:9298/orion/v1"

func newOrionProvider() *Definition {
	return &Definition{
		ID:           OrionID,
		Name:         "Orion",
		Names:        map[string]string{"zh-CN": "Orion"},
		Description:  "LoLR Orion model gateway: one OpenAI-compatible endpoint fronting every configured provider.",
		Descriptions: map[string]string{"zh-CN": "LoLR Orion 模型网关：一个 OpenAI 兼容入口统一分发全部已配置模型。"},
		Website:      "https://github.com/Tencent/WeKnora",
		Icon:         orionIcon,
		API:          api.APIOpenAICompletions,
		Order:        5,
		RequiresAuth: true,
		Auth:         AuthBearer,
		URLPatterns:  []string{"orion"},
		DefaultBaseURLs: map[types.ModelType]string{
			types.ModelTypeKnowledgeQA: OrionBaseURL,
			types.ModelTypeEmbedding:   OrionBaseURL,
			// The rerank client appends "/rerank", so the /v1 base is correct.
			types.ModelTypeRerank: OrionBaseURL,
			types.ModelTypeVLLM:   OrionBaseURL,
			types.ModelTypeASR:    OrionBaseURL,
		},
		ModelTypes: []types.ModelType{
			types.ModelTypeKnowledgeQA,
			types.ModelTypeEmbedding,
			types.ModelTypeRerank,
			types.ModelTypeVLLM,
			types.ModelTypeASR,
		},
		ExtraFields: []ExtraField{{
			Key:    models.ExtraScoreScale,
			Label:  "Rerank score scale",
			Labels: map[string]string{"zh-CN": "Rerank 分数标度"},
			Type:   "select",
			Options: []ExtraFieldOption{
				{
					Label:  "Unnormalized logits (apply sigmoid)",
					Labels: map[string]string{"zh-CN": "未归一化的 logit（应用 sigmoid）"},
					Value:  "logit",
				},
				{
					Label:  "Already normalized relevance (0..1)",
					Labels: map[string]string{"zh-CN": "已归一化的相关度（0~1）"},
					Value:  "probability",
				},
			},
			Placeholder: "match the upstream reranker output; bge-reranker behind vLLM usually reports probability",
			Placeholders: map[string]string{
				"zh-CN": "按 Orion 上游重排模型的输出选择；vLLM 上的 bge-reranker 一般为 probability",
			},
			Required:   false,
			ModelTypes: []types.ModelType{types.ModelTypeRerank},
		}},
		Compat: VendorCompat{
			// Orion forwards embeddings payloads to the upstream provider. It
			// does not document an encoding_format preference and some upstreams
			// reject unknown fields, so only the optional dimensions field is
			// named explicitly.
			Embeddings: api.EmbeddingsCompat{
				DimensionsField: api.Ptr("dimensions"),
			},
			OpenAICompletions: api.OpenAICompletionsCompat{},
		},
	}
}
