# PostgreSQL 的 pg_textsearch 适配

此二开在 v0.8.2 上保留 ParadeDB，并增加可选的 `pg_textsearch` 关键词检索。
应用连接地址由 `DB_HOST/DB_PORT/DB_USER/DB_PASSWORD/DB_NAME` 控制，切换关键词引擎由
`POSTGRES_SEARCH_EXTENSION` 控制。向量仍使用 pgvector，`RETRIEVE_DRIVER=postgres` 不变。

## 配置与索引

默认 `POSTGRES_SEARCH_EXTENSION=pg_search`，已有部署继续运行 ParadeDB。
目标数据库启用 `pg_textsearch` 时，设置：

```dotenv
DB_DRIVER=postgres
RETRIEVE_DRIVER=postgres
POSTGRES_SEARCH_EXTENSION=pg_textsearch
DB_SSLMODE=require
```

目标服务器须预加载 `pg_textsearch`，并提供 `vector`、`pg_trgm` 和 `uuid-ossp`。
服务器已经安装可用扩展后，应用迁移会建立表和索引。两个扩展都注册 `bm25` 访问方法，
不要在现有 ParadeDB 库内强行安装另一个扩展；使用独立的目标数据库。
`DB_SSLMODE` 同时应用于业务连接和迁移连接，本地默认 `disable`，公网托管数据库用
`require` 或配置好证书后的 `verify-full`。

迁移 `000111` 增加可空的 `embeddings.search_content`。启用新引擎后使用
`embeddings_textsearch_idx`，索引配置为 `simple`。写入和查询均复用应用已有的
Jieba 搜索分词器：保留词频，原始 `content` 和向量不变。此路径可以在
`pg_textsearch 1.3.1` 上工作，不要求数据库安装 zhparser。

单条、批量、复制索引均填写分词列；启用新引擎时，应用启动会按 500 条一批回填
`search_content IS NULL` 的历史记录。回填失败会停止初始化，避免在不完整索引上提供查询。
迁移失败或必要索引缺失时，新引擎模式也会停止初始化。分词字典通过现有
`JIEBA_DICT_DIR` 配置，变更字典后需要重新生成分词列并重建索引。

关键词查询保留知识库、文档、标签及启用状态过滤。先计算 top-k，再去掉零分的未匹配记录；
pg_textsearch 的负分转换为正分，以保持现有混合检索的“高分优先”约定。

## 独立验证

以下容器和数据库仅供测试，不指向线上库。镜像用于 Linux x86-64 的 PostgreSQL 17。
先下载固定版本的官方扩展发行包：

```bash
curl -fL https://github.com/timescale/pg_textsearch/releases/download/v1.3.1/pg_textsearch-pg17-v1.3.1.tar.gz \
  -o docker/pg_textsearch-pg17-v1.3.1.tar.gz
docker build -f docker/Dockerfile.pg-textsearch-test -t weknora-pg-textsearch-test:1.3.1 docker
task_initdb=$(mktemp -d)
docker run --name weknora-pg-textsearch-test -d \
  --mount type=bind,source="$task_initdb",target=/docker-entrypoint-initdb.d,readonly \
  -e POSTGRES_DB=weknora_textsearch_test -e POSTGRES_PASSWORD=test-only \
  -p 127.0.0.1:25432:5432 weknora-pg-textsearch-test:1.3.1
WEKNORA_TEXTSEARCH_TEST_DSN='postgres://postgres:test-only@127.0.0.1:25432/weknora_textsearch_test?sslmode=disable&options=-c%20app.postgres_search_extension=pg_textsearch' \
  go test ./internal/application/repository/retriever/postgres -count=1
```

基底保留 pgvector 的安装包；空的初始化脚本目录避免自动创建 pg_search 扩展。
Dockerfile 允许通过 `PG_IMAGE` 构建参数选择可信的同版镜像源。
测试覆盖完整建库迁移、中文查询、作用域过滤、禁用条目、无匹配结果、回填、复制、删除、
仅关键词写入以及向量读取。`internal/container` 中的 `TestInitDatabase*` 另验证应用启动、
回填及配置校验；运行前先完成上述检索测试以建立样本。
另有 `WEKNORA_PARADEDB_TEST_DSN` 回归旧检索引擎，限定数据库名为
`weknora_082_upgrade_test`。迁移共享空间的测试使用 `WEKNORA_UPGRADE_TEST_POSTGRES_DSN`，
同样限定该临时数据库名，验证升级前的空间名称和成员角色仍可读取。

## 部署顺序

先升级 v0.8.2 并验证现有数据库，再测试新引擎。新代码可以继续保持默认 ParadeDB 模式；
仅当独立目标数据库迁移与真实样本验证通过后，才修改连接地址与关键词引擎配置。
这次适配不负责自动转移源库到 RDS。业务数据迁移时需要保留密钥、共享空间和成员关系，
并在目标库重建索引；不能原样恢复 ParadeDB 的扩展和 BM25 索引。

2026-10-12 在独立 PostgreSQL 17.9 / pg_textsearch 1.3.1 / pgvector 0.8.1 环境验证以上功能；
旧 ParadeDB 0.22.6 路径与从迁移 90 升级到 111 的共享空间保留测试也通过。
这属于功能验证，尚未覆盖托管 RDS 的实际权限、网络延迟或大规模负载。
