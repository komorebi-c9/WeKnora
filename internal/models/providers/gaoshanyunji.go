// Package providers registers GaoShan YunJi (高山云基), a New API style
// gateway that fronts the Qwen family behind one OpenAI-compatible base URL.
//
// Facts (https://logosdata.cn):
//   - the base URL is the gateway root plus /v1
//     (https://logosdata.cn/new-api/v1) and serves
//     POST /v1/chat/completions, POST /v1/embeddings and POST /v1/rerank;
//   - auth is `Authorization: Bearer <API Key>` and there are no other
//     credentials or vendor fields;
//   - the chat models are the Qwen hybrid-thinking family (qwen3.5-plus,
//     qwen3.6-plus, qwen3.7-plus, qwen3.8-flash, qwen3.8-27b). Their thinking
//     switch is DashScope's `enable_thinking`, which the gateway forwards
//     upstream;
//   - text-embedding-v4 is the OpenAI-shaped embedding model under the same
//     base URL (/v1/embeddings);
//   - qwen3-vl-rerank is served on the Cohere-shaped rerank endpoint
//     (/v1/rerank), which is what the default rerank protocol speaks.
package providers

import (
	_ "embed"

	"github.com/Tencent/WeKnora/internal/models/api"
	"github.com/Tencent/WeKnora/internal/types"
)

//go:embed assets/gaoshanyunji.svg
var gaoshanyunjiIcon []byte

// GaoshanyunjiID is the provider identifier stored on model rows.
const GaoshanyunjiID = "gaoshanyunji"

// GaoshanyunjiBaseURL serves chat, embedding and rerank behind one gateway.
const GaoshanyunjiBaseURL = "https://logosdata.cn/new-api/v1"

func newGaoshanyunjiProvider() *Definition {
	return &Definition{
		ID:           GaoshanyunjiID,
		Name:         "GaoShan YunJi",
		Names:        map[string]string{"zh-CN": "高山云基 GaoShan YunJi"},
		Description:  "qwen3.5-plus, qwen3.8-flash, qwen3.8-27b, text-embedding-v4, qwen3-vl-rerank, etc.",
		Website:      "https://logosdata.cn",
		Icon:         gaoshanyunjiIcon,
		API:          api.APIOpenAICompletions,
		Order:        0,
		RequiresAuth: true,
		Auth:         AuthBearer,
		URLPatterns:  []string{"logosdata.cn"},
		DefaultBaseURLs: map[types.ModelType]string{
			types.ModelTypeKnowledgeQA: GaoshanyunjiBaseURL,
			types.ModelTypeEmbedding:   GaoshanyunjiBaseURL,
			types.ModelTypeRerank:      GaoshanyunjiBaseURL,
			// The Qwen chat models accept images, so the same gateway serves
			// the visual model type on the same base URL.
			types.ModelTypeVLLM: GaoshanyunjiBaseURL,
		},
		ModelTypes: []types.ModelType{
			types.ModelTypeKnowledgeQA,
			types.ModelTypeEmbedding,
			types.ModelTypeRerank,
			types.ModelTypeVLLM,
		},
		Compat: VendorCompat{
			OpenAICompletions: api.OpenAICompletionsCompat{
				// `max_tokens` is what this OpenAI-compatible gateway accepts;
				// `max_completion_tokens` is not part of its surface.
				MaxTokensField: api.Ptr("max_tokens"),
				// The Qwen family switches thinking with `enable_thinking`,
				// which the gateway passes through to the upstream vendor.
				ThinkingFormat: api.Ptr(api.ThinkingFormatEnableThinking),
			},
		},
	}
}
