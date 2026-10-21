# 公司统一知识库模型

设置 `KB_MODEL_POLICY=company`，并通过现有 `BUILTIN_MODELS_CONFIG` 机制加载
`config/company_models.yaml.example` 中的三个独立内置模型。实际密钥只存服务端私有配置或环境中。

新建文档/FAQ 知识库自动绑定 qwen3.7-text-embedding-flash（1024 维）、qwen3.7-flash、qwen3-vl-plus。
普通用户不能改这些绑定或通过上传覆盖视觉模型；系统管理员仍可维护内置模型。
未使用这些专用内置 ID 的旧库不受策略影响，不批量更新、不重建向量。

`GET /api/v1/knowledge-bases/model-policy` 只返回启用状态和模型 ID，不返回密钥。
关闭策略停止新建时自动绑定，已经绑定的公司知识库仍保留其模型保护。

配置复制（不带内容）使用新模型。带内容复制时，旧模型向量不能直接复制到新模型空间，
必须新建后重新导入；系统不会悄悄把原有向量当成 Flash 向量。

部署验收：普通 Contributor 新建时不用提交模型 ID；提交其他 ID 也被后端覆盖；
模型改绑请求返回 403；上传小 TXT 并确认 1024 维向量可检索；比对旧库模型绑定不变。
