package types

import "os"

// Dedicated IDs distinguish newly managed knowledge bases from legacy bindings.
const (
	CompanyEmbeddingModelID = "builtin-company-embedding"
	CompanySummaryModelID = "builtin-company-summary"
	CompanyVisionModelID = "builtin-company-vision"
)

func CompanyKBModelsEnabled() bool { return os.Getenv("KB_MODEL_POLICY") == "company" }

func (kb *KnowledgeBase) HasCompanyModels() bool {
	return kb != nil && kb.EmbeddingModelID == CompanyEmbeddingModelID
}

func BindCompanyKBModels(kb *KnowledgeBase) {
	kb.EmbeddingModelID = CompanyEmbeddingModelID
	kb.SummaryModelID = CompanySummaryModelID
	kb.VLMConfig = VLMConfig{Enabled: true, ModelID: CompanyVisionModelID}
}
