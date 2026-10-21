package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type companyModelStub struct {
	interfaces.ModelService
	models map[string]*types.Model
}

func (s companyModelStub) GetModelByID(_ context.Context, id string) (*types.Model, error) {
	return s.models[id], nil
}

func TestCompanyKBModels(t *testing.T) {
	svc := &knowledgeBaseService{modelService: companyModelStub{models: map[string]*types.Model{
		types.CompanyEmbeddingModelID: {IsBuiltin: true, Type: types.ModelTypeEmbedding, Status: types.ModelStatusActive},
		types.CompanySummaryModelID:   {IsBuiltin: true, Type: types.ModelTypeKnowledgeQA, Status: types.ModelStatusActive},
		types.CompanyVisionModelID:    {IsBuiltin: true, Type: types.ModelTypeVLLM, Status: types.ModelStatusActive},
	}}}
	t.Setenv("KB_MODEL_POLICY", "")
	legacy := &types.KnowledgeBase{EmbeddingModelID: "legacy", SummaryModelID: "old-summary"}
	require.NoError(t, svc.applyCompanyKBModels(context.Background(), legacy))
	require.Equal(t, "legacy", legacy.EmbeddingModelID)
	t.Setenv("KB_MODEL_POLICY", "company")
	kb := &types.KnowledgeBase{EmbeddingModelID: "client-override"}
	require.NoError(t, svc.applyCompanyKBModels(context.Background(), kb))
	require.Equal(t, types.CompanyEmbeddingModelID, kb.EmbeddingModelID)
	require.Equal(t, types.CompanySummaryModelID, kb.SummaryModelID)
	require.Equal(t, types.CompanyVisionModelID, kb.VLMConfig.ModelID)
	require.True(t, kb.VLMConfig.Enabled)
	require.Error(t, ValidateProcessOverrides(context.Background(), kb, &types.KnowledgeProcessOverrides{
		VLMConfig: &types.VLMConfig{Enabled: true, ModelID: "client-override"},
	}, []string{"png"}))
	require.NoError(t, ValidateProcessOverrides(context.Background(), kb, &types.KnowledgeProcessOverrides{
		VLMConfig: &types.VLMConfig{Enabled: true, ModelID: types.CompanyVisionModelID},
	}, []string{"png"}))
	require.Equal(t, "legacy", legacy.EmbeddingModelID)
	temporary := &types.KnowledgeBase{IsTemporary: true, EmbeddingModelID: "temporary"}
	require.NoError(t, svc.applyCompanyKBModels(context.Background(), temporary))
	require.Equal(t, "temporary", temporary.EmbeddingModelID)
	svc.modelService = companyModelStub{}
	require.Error(t, svc.applyCompanyKBModels(context.Background(), &types.KnowledgeBase{}))
}

func TestCompanyVisionUpdate(t *testing.T) {
	require.NoError(t, validateCompanyVisionUpdate(nil, nil))
	require.NoError(t, validateCompanyVisionUpdate(&types.KnowledgeBaseConfig{}, nil))
	require.NoError(t, validateCompanyVisionUpdate(nil, &types.VLMConfig{ModelID: types.CompanyVisionModelID}))
	require.Error(t, validateCompanyVisionUpdate(nil, &types.VLMConfig{ModelID: "other"}))
	require.Error(t, validateCompanyVisionUpdate(&types.KnowledgeBaseConfig{
		ImageProcessingConfig: &types.ImageProcessingConfig{ModelID: "other"},
	}, nil))
}
