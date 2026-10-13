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

2026-10-13 从腾讯云 CVM 经公网 TLS 1.3 连接阿里云按量付费 RDS PostgreSQL 17.11，
在新建的 `weknora_textsearch_test` 中完成迁移 1–111、应用数据库初始化、中文 BM25、
知识库/文档/标签过滤、禁用条目、关键词无向量写入、1024 维向量读写、复制、删除和历史回填测试。
目标扩展为 `pg_textsearch 1.3.1`、`vector 0.8.0.2`、`pg_trgm 1.6`、`uuid-ossp 1.1`。
测试连接使用 `sslmode=require`；它加密链路但不验证服务器身份，正式部署应配置 CA 并
使用 `verify-full`。本次未切换线上连接、迁移业务数据或运行压力测试。

## 隔离压测入口

`TestRDSRetrievalLoad` 是显式开启的合成数据压测，仅接受数据库名
`weknora_load_test` 和 `sslmode=require`。不要指向业务库。
先在 RDS 从已初始化的空测试库复制结构，清空复制来的测试 embeddings，
通过 `WEKNORA_LOAD_TEST_DSN` 注入连接信息（不要把密码写入仓库或命令日志）。

```bash
WEKNORA_LOAD_STAGE=seed go test ./internal/application/repository/retriever/postgres \
  -run '^TestRDSRetrievalLoad$' -count=1 -timeout=60m -v
go test ./internal/application/repository/retriever/postgres \
  -run '^TestRDSRetrievalLoad$' -count=1 -timeout=30m -v
```

准备阶段要求 embeddings 为空，生成 195143 条合成文本和随机 1024 维向量，
知识库分布为 131555/63566/22，文本平均约 361 字节、P95 约 1158 字节。
仅在这个隔离库重建 BM25/HNSW，建 HNSW 时单连接临时使用 128MB maintenance_work_mem，
不更改实例参数。向量在 RDS 内随机生成，不调用模型，也不代表真实语义分布。

检索复用生产 PostgreSQL repository，80% 查询最大知识库、20% 联合查询两个主要知识库，
TopK=10；向量 HNSW ef_search=100。
分别运行关键词、向量、双路并行的单并发 30 秒基线，再按 1/5/10/20 并发运行混合流量，
每档 180 秒。并发档出现错误或 P95 超过 2 秒时不再加压；单请求超时 5 秒，
连续累计至少 5 个错误且错误率超过 5% 时提前结束当前档。
最后在已通过的最高档追加每秒一次写入，持续 180 秒。
这里的并发是闭环不停请求的 worker 数，不是公司在线用户数。

每档输出请求数、错误数、QPS、P50/P95/P99/平均延迟和写入结果，
保存为运行目录的 `rds-load-results.json`。测量包含 CVM→RDS 跨云 TLS 往返，
不包含 HTTP、模型调用、重排、完整业务解析流程，也不是开放到达率的容量证明。
测试仅进入关键词和向量 repository，不实例化任何模型或同步任务。

### 2026-10-13 实测结果

按量 RDS 为 1核2GB，PostgreSQL 17.11。CVM 为 8核16GB，腾讯云只读
DescribeInstances API 确认公网出带宽上限为 5Mbps，测试期间总发送速率接近该上限。
样本实际文本平均 361 字节、P95 1161 字节，195143 条随机 1024 维向量；
建库与索引完成时约 1239MB，准备耗时 19分6秒。

单并发 30秒基线 P95：关键词 25ms、向量 101ms、双路并行 91ms，均零错误。
以下持续档每档约 3分钟，P95 包含跨云网络及连接等待：

| 场景 | 检索请求数 | 检索/秒 | 检索 P95 | 检索错误 |
| --- | ---: | ---: | ---: | ---: |
| 1并发混合 | 3119 | 17.3 | 91ms | 0 |
| 5并发混合 | 16094 | 89.4 | 99ms | 0 |
| 10并发混合 | 14953 | 83.0 | 403ms | 0 |
| 20并发混合 | 14207 | 77.9 | 919ms | 3 |
| 10并发混合＋写入 | 14825 | 81.8 | 409ms | 0 |

20并发的错误包含 DNS 解析超时及请求超时，错误后未继续提升并发。
边写边查中客户端确认 115 次写入成功、1 次写入超时；结束后库内可见 115 条 writer 记录。
因此不能将测试标记为全面通过；Go test 的 PASS 仅表示压测入口正常结束。
并发请求使用最多 48 条连接、8 条空闲连接，不是生产应用默认连接池的严格复刻。
生产代码此版本配置 10 条空闲连接、10分钟连接寿命，不限制 PostgreSQL 最大连接数；
正式部署仍需根据 RDS 限额与其他业务连接合理控制连接池。

压测结束后的只读执行计划确认 BM25 与 1024维 HNSW 索引被使用；
两个单次热缓存查询的数据库执行时间分别约 5.1ms、7.7ms，后者设置 ef_search=100。
这些单次结果不代表并发期间的数据库 P95。
本次未取得 RDS CPU/内存峰值，且 pg_stat_statements.track 默认 none、账号无修改权限，
未为压测提权或修改实例参数。5Mbps 链路已构成重要约束，但不能证明全部超时只有这一个原因。

结论：低并发检索功能和性能可用；当前跨云部署在高并发及边写边查下仍出现超时，
不能据此承诺生产稳定性，也不能单凭本轮结果判断必须升级 RDS CPU/内存。
未切换或写入生产数据库，未调用模型。随机合成数据、热缓存、闭环负载及短时间测试
不能替代真实业务、长时间浸泡和完整 HTTP/解析流程验收。
