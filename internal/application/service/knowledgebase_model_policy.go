package service

import (
	"context"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
)

func validateCompanyVisionUpdate(config *types.KnowledgeBaseConfig, vlm *types.VLMConfig) error {
	if config != nil && config.ImageProcessingConfig != nil &&
		config.ImageProcessingConfig.ModelID != "" && config.ImageProcessingConfig.ModelID != types.CompanyVisionModelID {
		return apperrors.NewForbiddenError("该知识库的视觉模型由公司统一配置")
	}
	if vlm != nil && vlm.ModelID != "" && vlm.ModelID != types.CompanyVisionModelID {
		return apperrors.NewForbiddenError("该知识库的视觉模型由公司统一配置")
	}
	return nil
}

func (s *knowledgeBaseService) applyCompanyKBModels(ctx context.Context, kb *types.KnowledgeBase) error {
	if !types.CompanyKBModelsEnabled() || kb.IsTemporary {
		return nil
	}
	for _, binding := range []struct {
		id   string
		kind types.ModelType
	}{
		{types.CompanyEmbeddingModelID, types.ModelTypeEmbedding},
		{types.CompanySummaryModelID, types.ModelTypeKnowledgeQA},
		{types.CompanyVisionModelID, types.ModelTypeVLLM},
	} {
		model, err := s.modelService.GetModelByID(ctx, binding.id)
		if err != nil || model == nil || !model.IsBuiltin || model.Type != binding.kind || model.Status != types.ModelStatusActive {
			return apperrors.NewBadRequestError("公司统一模型尚未配置完成，请联系管理员")
		}
	}
	types.BindCompanyKBModels(kb)
	return nil
}
